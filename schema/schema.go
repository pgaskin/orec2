package schema

//go:generate go run github.com/bufbuild/buf/cmd/buf@v1.50.1 generate --template {"version":"v2","plugins":[{"local":["go","tool","protoc-gen-go"],"out":".","opt":["paths=source_relative","Mschema.proto=./schema"]}]}
