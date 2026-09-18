The cmd/sessile-mcp bridge, cross-compiled for each target sessile uploads
it to, lands here at build time (`make bridge`, and the Dockerfile's backend
stage) and is embedded into the server. The binaries are not committed.
Without them the server still builds and runs; tasks just get no tools.
