module github.com/mirzakhany/dbctl/clients/dbctlgo

go 1.21.0

require (
	github.com/gomodule/redigo v1.8.9
	github.com/lib/pq v1.10.9
	github.com/mirzakhany/clients/dbctlgo v0.0.0-unpublished
)

replace github.com/mirzakhany/clients/dbctlgo v0.0.0-unpublished => ./
