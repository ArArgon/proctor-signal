package model

//go:generate protoc -I ../ --proto_path=./ --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative model.proto
