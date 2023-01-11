package backend

//go:generate protoc --proto_path=../../proto/ --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative auth_service.proto
//go:generate protoc --proto_path=../../proto/ --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative problem_service.proto
//go:generate protoc --proto_path=../../proto/ --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative resource_service.proto
//go:generate protoc --proto_path=../../proto/ --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative backend.proto

//go:generate mockgen -destination=mock/backend_mock.go . Client
