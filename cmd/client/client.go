/*
 * This is free and unencumbered software released into the public domain.
 *
 * Anyone is free to copy, modify, publish, use, compile, sell, or
 * distribute this software, either in source code form or as a compiled
 * binary, for any purpose, commercial or non-commercial, and by any
 * means.
 *
 * In jurisdictions that recognize copyright laws, the author or authors
 * of this software dedicate any and all copyright interest in the
 * software to the public domain. We make this dedication for the benefit
 * of the public at large and to the detriment of our heirs and
 * successors. We intend this dedication to be an overt act of
 * relinquishment in perpetuity of all present and future rights to this
 * software under copyright law.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS BE LIABLE FOR ANY CLAIM, DAMAGES OR
 * OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE,
 * ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR
 * OTHER DEALINGS IN THE SOFTWARE.
 *
 * For more information, please refer to <https://unlicense.org>
 */

package main

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/radovskyb/watcher"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	flag "github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/khannz/pdb/proto"
)

var (
	path       = flag.StringP("path", "p", "", "path to use as sync source (required)")
	serverAddr = flag.StringP("server", "s", "localhost:8080", "The server address in the format of host:port")
	chunkSize  = flag.IntP("chunk-size", "c", 4096, "Chunk size in bytes")

	// this vars would be used at LDFLAGS by goreleaser or something
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

//type Chunk struct {
//	path     string
//	id       int
//	offset   int
//	checksum string
//}

type File struct {
	path     string
	mode     string
	checksum string
	//chunks   []Chunk
}

type ContentWatcher struct {
	//files      map[string]string
	grpcClient proto.DropboxClient
	root       string
	watcher    *watcher.Watcher
	files      []File
}

// NewContentWatcher function creates new ContentWatcher
func NewContentWatcher(w *watcher.Watcher, c proto.DropboxClient, r string) *ContentWatcher {
	return &ContentWatcher{
		grpcClient: c,
		root:       r,
		watcher:    w,
	}
}

// Start method makes some basic checks regarding source dir existence and starts cw.watcher
func (cw *ContentWatcher) Start() {
	// check if root flag was empty. Empty string works like we pass run context, but since that is not obvious
	// behavior, better to avoid it
	if cw.root == "" {
		log.Fatal().Msg("root flag can't be empty")
	}

	// check if root path exist (create it otherwise) and IsDir (fatal otherwise)
	if stat, err := os.Stat(cw.root); err != nil {
		if err := os.Mkdir(cw.root, 0755); err != nil {
			log.Fatal().Err(err).Msg("error while creating root directory")
		}
		log.Warn().Msg("root path was created")
	} else if stat.IsDir() {
		log.Debug().Msg("root path exist")
	} else if !stat.IsDir() {
		log.Error().Msg("root path is a file")
	}

	if err := cw.watcher.AddRecursive(cw.root); err != nil {
		log.Fatal().Err(err).Msg("start failed at AddRecursive")
	}

	if err := cw.watcher.Start(100 * time.Millisecond); err != nil { // FIXME: gomnd
		log.Fatal().Err(err).Msg("can't start watcher")
	}
}

// RescanTree method rescans directories structure under root path
func (cw *ContentWatcher) RescanTree() error {
	if err := cw.watcher.AddRecursive(cw.root); err != nil {
		return err
	}
	log.Trace().Msg("RescanTree success")

	return nil
}

// result is the product of reading and summing a file using MD5
type result struct {
	path string
	mode string
	sum  string
	err  error
}

// sumFiles starts goroutines to walk the directory tree at root and digest each
// regular file. These goroutines send the results of digest  on the result
// channel and send the result of the walk on the error channel. If done is
// closed, sumFiles abandons its work.
func sumFiles(done <-chan struct{}, root string) (<-chan result, <-chan error) {
	// Fore each regular file, start a goroutine that sums the file and sends
	// the result on c. Send the result of the walk on errc.
	c := make(chan result)
	errc := make(chan error, 1)
	go func() {
		var wg sync.WaitGroup
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			wg.Add(1)
			go func() {
				data, err := ioutil.ReadFile(path)
				select {
				case c <- result{path, info.Mode().Perm().String(), fmt.Sprintf("%x", md5.Sum(data)), err}:
				case <-done:
				}
				wg.Done()
			}()
			// Abort the walk if done is closed.
			select {
			case <-done:
				return errors.New("walk canceled")
			default:
				return nil
			}
		})
		// Walk has returned, so all calls to wg.Add are done. Start a
		// goroutine to close c once all the sends are done.
		go func() {
			wg.Wait()
			close(c)
		}()
		// No select needed here, since errc is buffered
		errc <- err
	}()
	return c, errc
}

// MD5All reads all the files in the file tree rooted at root and returns a map
// from file path to the MD5 sum of the file's contents. If the directory walk
// fails or any read operation fails, MD5All returns an error. In that case,
// MD5All does not wait for inflight read operations to complete.
func MD5All(root string) ([]File, error) {
	// MD5All closes the done channel when it returns; it may do so before
	// receiving all the values from c and errc.
	done := make(chan struct{})
	defer close(done)

	c, errc := sumFiles(done, root)

	var files []File
	for r := range c {
		if r.err != nil {
			return nil, r.err
		}
		f := File{
			checksum: r.sum,
			mode:     r.mode,
			path:     r.path,
		}
		files = append(files, f)
	}
	if err := <-errc; err != nil {
		return nil, err
	}
	return files, nil
}

// WalkTree method ...
func (cw *ContentWatcher) WalkTree() {
	m, err := MD5All(cw.root)
	if err != nil {
		log.Error().Err(err).Msg("error while MD5All")
	}

	cw.files = m
	//fmt.Println(cw.files)
	log.Trace().Msg("method WalkTree executed")
}

// RemoveFile method ...
func (cw *ContentWatcher) RemoveFile() {
	log.Trace().Msg("method RemoveFile executed")
}

// Run method ...
func (cw *ContentWatcher) Run() {
	log.Info().Msg("starting watcher")
	for {
		select {
		case event := <-cw.watcher.Event:
			switch event.IsDir() {
			case false:
				cw.WalkTree()                        // TODO: make more granular actions
				grpcSayDiff(cw.grpcClient, cw.files) // TODO: make more granular actions

				switch event.Op {
				case watcher.Create:
					log.Trace().Str("raw", event.String()).Msg("file created")
					//cw.WalkTree()
				case watcher.Remove:
					log.Trace().Str("raw", event.String()).Msg("file removed")
					//cw.RemoveFile()
				case watcher.Write:
					log.Trace().Str("raw", event.String()).Msg("file written")
				case watcher.Move:
					log.Trace().Str("raw", event.String()).Msg("file moved")
				case watcher.Rename:
					log.Trace().Str("raw", event.String()).Msg("file renamed")
				case watcher.Chmod:
					log.Trace().Str("raw", event.String()).Msg("file permissions changed")
				default:
					log.Trace().Str("raw", event.String()).Msg("unhandled event detected")
				}
			case true:
				switch event.Op {
				case watcher.Create:
					log.Trace().Str("raw", event.String()).Msg("directory created")
					if err := cw.RescanTree(); err != nil {
						log.Fatal().Err(err).Msg("can't RescanTree")
					}
				case watcher.Remove:
					log.Trace().Str("raw", event.String()).Msg("directory removed")
					// TODO: investigate if need to update .AddRecursive after path deletion
				case watcher.Write:
					log.Trace().Str("raw", event.String()).Msg("directory written")
				case watcher.Move:
					log.Trace().Str("raw", event.String()).Msg("directory moved")
				case watcher.Rename:
					log.Trace().Str("raw", event.String()).Msg("directory renamed")
				case watcher.Chmod:
					log.Trace().Str("raw", event.String()).Msg("directory permissions changed")
				default:
					log.Trace().Str("raw", event.String()).Msg("unhandled event detected")
				}
			}
		case err := <-cw.watcher.Error:
			log.Fatal().Err(err)
		case <-cw.watcher.Closed:
			return
		}
	}
}

func grpcSayDiff(client proto.DropboxClient, request []File) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // FIXME: gomnd
	defer cancel()

	var files []*proto.File
	for _, file := range request {
		f := &proto.File{
			Checksum: file.checksum,
			Mode:     file.mode,
			Path:     file.path,
		}
		files = append(files, f)
	}

	response, err := client.SayDiff(ctx, &proto.DiffRequest{
		Files: files,
	})

	if err != nil {
		errStatus, _ := status.FromError(err)
		switch errStatus.Code() {
		case codes.Unavailable:
			log.Error().Err(err).Msg("pexip-dropbox-server unavailable")
		case codes.Unimplemented:
			log.Error().Err(err).Msg("pexip-dropbox-server no implemented that RPC yet")
		default:
			log.Error().Err(err).Msg("something broken")
		}
	}
	log.Info().Msgf("grpcSayDiff last line %v", response)
}

func main() {
	flag.Parse()
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout})

	var opts []grpc.DialOption
	opts = append(opts, grpc.WithInsecure())
	conn, err := grpc.Dial(*serverAddr, opts...)
	if err != nil {
		log.Fatal().Msgf("fail to dial: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			log.Error().Err(err).Msg("defer on grpc.Dial failed")
		}
	}()
	client := proto.NewDropboxClient(conn)

	cw := NewContentWatcher(watcher.New(), client, *path)

	go cw.Run()
	log.Info().
		Str("chunk-size", strconv.Itoa(*chunkSize)).
		Str("root", *path).
		Str("server", *serverAddr).
		Msg("pexip-dropbox-client started")
	cw.Start()
}
