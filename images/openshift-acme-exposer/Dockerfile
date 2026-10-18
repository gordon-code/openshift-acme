# ubi9/go-toolset:1.26.7 (1.26.7-1792010711)
FROM registry.access.redhat.com/ubi9/go-toolset:1.26.7@sha256:60fd6fb2998c40ad0d6561c34e83588b986a0cf7771f64e5e5c295ba87101f9b AS builder
ENV GOTOOLCHAIN=auto
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -o openshift-acme-exposer ./cmd/openshift-acme-exposer

# ubi9/ubi-minimal:9.8-1791898448
FROM registry.access.redhat.com/ubi9/ubi-minimal:latest@sha256:c4c285d42941ebb2bd4da878479008e1e4536bb599aad1dbe568f89de9053661
COPY --from=builder /workspace/openshift-acme-exposer /usr/bin/openshift-acme-exposer
USER 1001
ENTRYPOINT ["/usr/bin/openshift-acme-exposer"]
