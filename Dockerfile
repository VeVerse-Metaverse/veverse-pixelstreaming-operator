############ BUILD ####################
FROM golang:1.19 as builder

#ENV GO111MODULE=off

# Make a directory for the app
RUN mkdir -p $GOPATH/src/dev.hackerman.me/artheon/veverse-pixelstreaming-operator

# Copy all source files into the app directory
COPY \
database.go \
ec2api.go \
logger.go \
main.go \
model.go \
service.go \
go.mod \
go.sum \
$GOPATH/src/dev.hackerman.me/artheon/veverse-pixelstreaming-operator/
COPY reflect $GOPATH/src/dev.hackerman.me/artheon/veverse-pixelstreaming-operator/reflect

WORKDIR $GOPATH/src/dev.hackerman.me/artheon/veverse-pixelstreaming-operator
RUN pwd && ls -lah

# Authorize SSH Host
RUN mkdir -p /root/.ssh && \
    chmod 0700 /root/.ssh && \
    ssh-keyscan gitlab.com > /root/.ssh/known_hosts

# Add SSH configuration for the root user for gitlab.com
RUN echo "Host gitlab.com\
        HostName gitlab.com\
        User root\
        IdentityFile ~/.ssh/id_rsa\
        ForwardAgent yes" > ~/.ssh/config

# Add authorized SSH keys for the builder account
ENV PUBLIC_KEY="ssh-rsa == builder@veverse.com"
ENV PRIVATE_KEY="-----BEGIN OPENSSH PRIVATE KEY-----\n\
-----END OPENSSH PRIVATE KEY-----"

# Add the keys and set permissions
RUN echo $PRIVATE_KEY > /root/.ssh/id_rsa && \
    echo $PUBLIC_KEY > /root/.ssh/id_rsa.pub && \
    chmod 600 /root/.ssh/id_rsa && \
    chmod 600 /root/.ssh/id_rsa.pub

# Start ssh-agent
RUN eval `ssh-agent -s` && \
    ssh-add ~/.ssh/id_rsa

# Configure git
RUN git config --global url."git@gitlab.com:".insteadOf "https://gitlab.com/"
RUN git config --global url."git@dev.hackerman.me:".insteadOf "https://dev.hackerman.me/"

# Download required dependencies
RUN go mod tidy

# Build
RUN CGO_ENABLED=0 GO111MODULE=on go build -o /veverse-pixelstreaming-operator

# Remove ssh keys
RUN rm -rf /root/.ssh/

############ RUN ####################
FROM alpine:3.8

COPY --from=builder /veverse-pixelstreaming-operator /usr/local/bin/

RUN ls -lah /usr/local/bin/

WORKDIR /tmp

RUN ls -lah /usr/local/bin/

ENTRYPOINT [ "/usr/local/bin/veverse-pixelstreaming-operator" ]