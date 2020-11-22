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
	"fmt"
	"net"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	flag "github.com/spf13/pflag"
	"google.golang.org/grpc"

	"github.com/khannz/pdb/proto"
)

var (
	address = flag.StringP("address", "A", "localhost", "address to bind to")
	port    = flag.IntP("port", "P", 8080, "port to bind to")
	path    = flag.StringP("path", "p", "", "path to use as sync target")
	force   = flag.BoolP("force", "f", false, "use path even if there is content")

	// this vars would be used at LDFLAGS by goreleaser or something
	version = "dev"
	commit  = "none"
	date    = "unknown"
	builtBy = "unknown"
)

type dropboxServer struct {
	proto.UnimplementedDropboxServer
}

func (s *dropboxServer) SayDiff(ctx context.Context, dreq *proto.DiffRequest) (*proto.DiffResponse, error) {
	fmt.Println(dreq)
	return &proto.DiffResponse{}, nil
}

func newServer() *dropboxServer {
	s := &dropboxServer{}
	return s
}

func init() {
	flag.Parse()
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout})
}

func main() {
	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", *address, *port))
	if err != nil {
		log.Fatal().
			Str("address", *address).
			Str("port", string(*port)).
			Msgf("failed to listen: %v", err)
	}

	var opts []grpc.ServerOption
	grpcServer := grpc.NewServer(opts...)
	proto.RegisterDropboxServer(grpcServer, newServer())

	log.Info().
		Str("address", fmt.Sprintf("%s:%d", *address, *port)).
		Str("path", *path).
		Msg("pexip-dropbox-server started")

	if err := grpcServer.Serve(lis); err != nil {
		log.Error().Err(err).Msg("pexip-dropbox-server can't serve")
	}
}
