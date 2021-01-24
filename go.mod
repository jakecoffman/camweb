module github.com/jakecoffman/camweb

go 1.14

require (
	github.com/deepch/vdk v0.0.0-20210109142448-33b07c6a20f1
	github.com/gin-gonic/gin v1.6.3
	github.com/gorilla/websocket v1.4.2
	github.com/pion/webrtc/v3 v3.0.1
	github.com/xinsnake/go-http-digest-auth-client v0.6.0
)

replace github.com/xinsnake/go-http-digest-auth-client => github.com/jakecoffman/go-http-digest-auth-client v0.6.1-0.20210124024035-f8b71c7c172c
