.PHONY: all build tools clean test

TOOLS = changelog_extractor parse_log request_getter write-report
TOOL_BINS = $(addprefix bin/, $(TOOLS))

all: build tools

build: bin/osc-mcp

bin/osc-mcp:
	@mkdir -p bin
	go build -o $@ osc-mcp.go

tools: $(TOOL_BINS)

bin/%: tools/%/main.go
	@mkdir -p bin
	go build -o $@ ./tools/$*

test:
	go test ./...

clean:
	rm -rf bin/
