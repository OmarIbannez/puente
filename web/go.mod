// Marks web/ as a separate module so ./... in the Go module skips web/node_modules,
// which ships Go sources from npm packages.
module github.com/OmarIbannez/puente/web

go 1.27
