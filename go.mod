module github.com/rpop-project/rpop

go 1.26.0

require (
	github.com/mattn/go-sqlite3 v1.14.24
	go.uber.org/zap v1.27.0
	golang.org/x/net v0.59.0
)

require go.uber.org/multierr v1.10.0 // indirect

ignore ./web/node_modules
