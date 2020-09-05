# FROM dgraph/dgraph:latest

# RUN mkdir /backup
# COPY ./dgraph-backup /backup/dgraph-backup
# WORKDIR /backup
# CMD ["./dgraph-backup"]


FROM golang:1.14

# Set the Current Working Directory inside the container
WORKDIR $GOPATH/src/github.com/AugustDev/dgraph-backup-cli

# Copy everything from the current directory to the PWD (Present Working Directory) inside the container
COPY . .

# Download all the dependencies
RUN go get -d -v ./...

# Install the package
RUN go install -v ./...

# Run
CMD ["dgraph-backup-cli"]