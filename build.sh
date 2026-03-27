#!/bin/bash
CGO_CFLAGS="-I/opt/homebrew/opt/hackrf/include" CGO_LDFLAGS="-L/opt/homebrew/opt/hackrf/lib" go build -o hackdvbs .
