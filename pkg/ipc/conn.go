// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ipc

import (
	"context"
	"net"
	"path"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/livekit/protocol/logger"
)

const (
	network        = "unix"
	handlerAddress = "handler_ipc.sock"
	serviceAddress = "service_ipc.sock"
)

func StartServiceListener(ipcServer *grpc.Server, serviceTmpDir string) error {
	listener, err := net.Listen(network, path.Join(serviceTmpDir, serviceAddress))
	if err != nil {
		return err
	}

	go func() {
		if err = ipcServer.Serve(listener); err != nil {
			logger.Errorw("failed to start grpc handler", err)
		}
	}()

	return nil
}

func NewHandlerClient(handlerTmpDir string) (EgressHandlerClient, error) {
	socketAddr := path.Join(handlerTmpDir, handlerAddress)
	conn, err := grpc.Dial(socketAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, addr string) (net.Conn, error) {
			return net.Dial(network, addr)
		}),
	)
	if err != nil {
		logger.Errorw("could not dial grpc handler", err)
		return nil, err
	}

	return NewEgressHandlerClient(conn), nil
}

func StartHandlerListener(ipcServer *grpc.Server, handlerTmpDir string) error {
	listener, err := net.Listen(network, path.Join(handlerTmpDir, handlerAddress))
	if err != nil {
		return err
	}

	go func() {
		if err = ipcServer.Serve(listener); err != nil {
			logger.Errorw("failed to start grpc handler", err)
		}
	}()

	return nil
}

func NewServiceClient(serviceTmpDir string) (EgressServiceClient, error) {
	socketAddr := path.Join(serviceTmpDir, serviceAddress)
	conn, err := grpc.Dial(socketAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, addr string) (net.Conn, error) {
			return net.Dial(network, addr)
		}),
	)
	if err != nil {
		logger.Errorw("could not dial grpc handler", err)
		return nil, err
	}

	return NewEgressServiceClient(conn), nil
}
