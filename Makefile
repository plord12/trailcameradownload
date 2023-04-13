NAME=trailcameradownload
BINDIR=bin
SOURCES=$(wildcard *.go)
#BINARIES=${BINDIR}/${NAME}-darwin-amd64 ${BINDIR}/${NAME}-darwin-arm64 ${BINDIR}/${NAME}-darwin ${BINDIR}/${NAME}-linux-amd64 ${BINDIR}/${NAME}-linux-arm64 ${BINDIR}/${NAME}-linux-arm ${BINDIR}/${NAME}-windows.exe
BINARIES=${BINDIR}/${NAME}-darwin-arm64 # ${BINDIR}/${NAME}-linux-amd64 ${BINDIR}/${NAME}-linux-arm64 ${BINDIR}/${NAME}-linux-arm

all: ${BINDIR} ${BINARIES}

${BINDIR}:
	mkdir -p ${BINDIR}
	
${BINDIR}/${NAME}-darwin-amd64: ${SOURCES}
	GOARCH=amd64 GOOS=darwin go build -o $@

# also needs at runtime export DYLD_LIBRARY_PATH=$DYLD_LIBRARY_PATH:$HOME/src/tensorflow/tflite_build
${BINDIR}/${NAME}-darwin-arm64: ${SOURCES}
	CGO_CFLAGS=-I${HOME}/src/tensorflow/tensorflow_src CGO_LDFLAGS=-L${HOME}/src/tensorflow/tflite_build GOARCH=arm64 GOOS=darwin go build -o $@

${BINDIR}/${NAME}-darwin: ${BINDIR}/${NAME}-darwin-amd64 ${BINDIR}/${NAME}-darwin-arm64
	makefat $@ $^

${BINDIR}/${NAME}-linux-amd64: ${SOURCES}
	GOARCH=amd64 GOOS=linux go build -o $@

${BINDIR}/${NAME}-linux-arm64: ${SOURCES}
	CGO_LDFLAGS=-L/usr/local/lib GOARCH=arm64 GOOS=linux go build -o $@

${BINDIR}/${NAME}-linux-arm: ${SOURCES}
	GOARCH=arm GOOS=linux go build -o $@

${BINDIR}/${NAME}-windows.exe: ${SOURCES}
	GOARCH=amd64 GOOS=windows go build -o $@

run:
	go run ${SOURCES}

test:
	DYLD_LIBRARY_PATH=${DYLD_LIBRARY_PATH}:${HOME}/src/tensorflow/tflite_build CGO_CFLAGS=-I${HOME}/src/tensorflow/tensorflow_src CGO_LDFLAGS=-L${HOME}/src/tensorflow/tflite_build go test -v

cov:
	DYLD_LIBRARY_PATH=${DYLD_LIBRARY_PATH}:${HOME}/src/tensorflow/tflite_build CGO_CFLAGS=-I${HOME}/src/tensorflow/tensorflow_src CGO_LDFLAGS=-L${HOME}/src/tensorflow/tflite_build go test -coverprofile=coverage.out
	go tool cover -html=coverage.out

clean:
	@go clean
	-@rm -rf ${BINDIR} 2>/dev/null || true
