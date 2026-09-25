# ubi9/go-toolset:1.25 (1.25.9-1778675823)
FROM registry.access.redhat.com/ubi9/go-toolset:1.25@sha256:90a36bc2013b3fcb28e2a4b082c9b895d7c2c679e58b95aed9721970f3339d0e AS builder
WORKDIR /go/src/github.com/tnozicka/openshift-acme
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -o openshift-acme-exposer ./cmd/openshift-acme-exposer

# ubi9/ubi-minimal:9.8-1787647261
FROM registry.access.redhat.com/ubi9/ubi-minimal@sha256:580752f96d36c4132bffd30f9c34865bf4bd87f6aa161c969d117f21732e50f7
COPY --from=builder /go/src/github.com/tnozicka/openshift-acme/openshift-acme-exposer /usr/bin/openshift-acme-exposer
USER 1001
ENTRYPOINT ["/usr/bin/openshift-acme-exposer"]
