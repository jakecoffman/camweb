package camweb

import (
	"bytes"
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"log"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func ServeHTTP() {
	router := gin.Default()
	router.GET("/", func(context *gin.Context) {
		context.File("index.html")
	})
	router.GET("/ws", connect)
	router.GET("/streams", func(c *gin.Context) {
		c.JSON(200, config.Streams)
	})
	router.GET("/streams/:id", func(c *gin.Context) {
		if streams, ok := config.Streams[c.Param("id")]; ok {
			c.JSON(200, streams.Codecs)
		} else {
			c.JSON(404, "No codecs found")
		}
	})
	router.POST("/control/:id", func(c *gin.Context) {
		type Command struct {
			Cmd string `json:"cmd" binding:"eq=start|eq=stop"`
			Dir string `json:"dir" binding:"eq=Left|eq=Right|eq=Up|eq=Down"`
		}
		var cmd Command
		if err := c.BindJSON(&cmd); err != nil {
			c.AbortWithStatusJSON(400, "Bad request: "+err.Error())
			return
		}
		if streams, ok := config.Streams[c.Param("id")]; ok {
			url := streams.URL
			if err := control(c, url, cmd.Cmd, cmd.Dir); err != nil {
				c.AbortWithStatusJSON(500, err.Error())
				return
			}
			c.JSON(200, "OK")
		} else {
			c.JSON(404, "No codecs found")
		}
	})
	{
		debug := router.Group("/debug")
		debug.GET("/pprof/", gin.WrapF(pprof.Index))
		debug.GET("/pprof/cmdline", gin.WrapF(pprof.Cmdline))
		debug.GET("/pprof/profile", gin.WrapF(pprof.Profile))
		debug.GET("/pprof/symbol", gin.WrapF(pprof.Symbol))
		debug.GET("/pprof/goroutine", gin.WrapH(pprof.Handler("goroutine")))
		debug.GET("/pprof/heap", gin.WrapH(pprof.Handler("heap")))
		debug.GET("/pprof/threadcreate", gin.WrapH(pprof.Handler("threadcreate")))
		debug.GET("/pprof/block", gin.WrapH(pprof.Handler("block")))
	}
	fmt.Printf("http://127.0.0.1%v\n", config.Server.HTTPPort)
	err := router.Run(config.Server.HTTPPort)
	if err != nil {
		log.Fatalln("Start HTTP Server error", err)
	}
}

func connect(c *gin.Context) {
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}

	type Payload struct {
		ConnectRequest *struct {
			ID  string `json:"id"`
			SDP string `json:"sdp"`
		} `json:"connectionRequest"`
		Candidate *webrtc.ICECandidateInit `json:"candidate"`
	}

	var peerConnection *webrtc.PeerConnection
	for {
		var payload Payload
		if err = ws.ReadJSON(&payload); err != nil {
			return
		}
		if payload.Candidate != nil {
			if err = peerConnection.AddICECandidate(*payload.Candidate); err != nil {
				log.Println("Failed adding ICE Candidate", err, payload.Candidate)
				return
			}
		} else if payload.ConnectRequest != nil {
			if err = ws.ReadJSON(&payload); err != nil {
				log.Println(err)
				return
			}

			stream, ok := config.Streams[payload.ConnectRequest.ID]
			if !ok {
				log.Println("stream", payload.ConnectRequest.ID, "not found")
				return
			}

			mediaEngine := &webrtc.MediaEngine{}
			if err = RegisterCodecs(mediaEngine); err != nil {
				log.Println("RegisterCodecs error", err)
				return
			}
			api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

			if peerConnection == nil {
				peerConnection, err = api.NewPeerConnection(webrtc.Configuration{})
				if err != nil {
					log.Println("NewPeerConnection error", err)
					return
				}
			}

			var wsWriteLock sync.Mutex
			peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
				if candidate == nil {
					return
				}
				wsWriteLock.Lock()
				if err := ws.WriteJSON(candidate.ToJSON()); err != nil {
					log.Println("Failed sending ICE candidate", err)
				}
				wsWriteLock.Unlock()
			})

			if stream.VideoTrack == nil {
				log.Println("Error: VideoTrack not setup")
				return
			}

			rtpSender, err := peerConnection.AddTrack(stream.VideoTrack)
			if err != nil {
				log.Println("AddTrack error", err)
				return
			}

			// Read incoming RTCP packets
			// Before these packets are returned they are processed by interceptors. For things
			// like NACK this needs to be called.
			go func() {
				rtcpBuf := make([]byte, 1500)
				for {
					if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
						return
					}
				}
			}()

			if stream.AudioTrack != nil {
				audioRtpSender, err := peerConnection.AddTrack(stream.AudioTrack)
				if err != nil {
					log.Println(err)
					return
				}
				go func() {
					rtcpBuf := make([]byte, 1500)
					for {
						if _, _, rtcpErr := audioRtpSender.Read(rtcpBuf); rtcpErr != nil {
							return
						}
					}
				}()
			}

			offer := webrtc.SessionDescription{
				Type: webrtc.SDPTypeOffer,
				SDP:  payload.ConnectRequest.SDP,
			}
			if err := peerConnection.SetRemoteDescription(offer); err != nil {
				log.Println("SetRemoteDescription error", err, offer.SDP)
				return
			}
			answer, err := peerConnection.CreateAnswer(nil)
			if err != nil {
				log.Println("CreateAnswer error", err)
				return
			}

			if err = peerConnection.SetLocalDescription(answer); err != nil {
				log.Println("SetLocalDescription error", err)
				return
			}

			wsWriteLock.Lock()
			if err = ws.WriteJSON(map[string]string{
				"sdp": peerConnection.LocalDescription().SDP,
			}); err != nil {
				log.Println("Failed sending SDP", err)
			}
			wsWriteLock.Unlock()

			// remote audio track for voice
			peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
				log.Println("Got audio track", track.Codec().MimeType) // this says opus though I requested PCMA
				if track.Codec().MimeType != "audio/PCMA" {
					return
				}

				var voiceData bytes.Buffer
				go func() {
					time.Sleep(5 * time.Second)
					log.Println("Sending", voiceData.Len())
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err = say(ctx, stream.URL, voiceData); err != nil {
						return
					}
					voiceData.Reset()
				}()

				for {
					buf := make([]byte, 1500)
					n, _, err := track.Read(buf)
					if err != nil {
						log.Println(err)
						return
					}
					voiceData.Write(buf[:n])
				}
			})
		}
	}
}

// RegisterCodecs forces PCMA audio, the rest is default
func RegisterCodecs(m *webrtc.MediaEngine) error {
	for _, codec := range []webrtc.RTPCodecParameters{
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA, ClockRate: 8000},
			PayloadType:        8,
		},
	} {
		if err := m.RegisterCodec(codec, webrtc.RTPCodecTypeAudio); err != nil {
			return err
		}
	}

	// Default Pion Audio Header Extensions
	for _, extension := range []string{
		"urn:ietf:params:rtp-hdrext:sdes:mid",
		"urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id",
		"urn:ietf:params:rtp-hdrext:sdes:repaired-rtp-stream-id",
	} {
		if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: extension}, webrtc.RTPCodecTypeAudio); err != nil {
			return err
		}
	}

	videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"nack", ""}, {"nack", "pli"}}
	for _, codec := range []webrtc.RTPCodecParameters{
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: videoRTCPFeedback},
			PayloadType:        96,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, SDPFmtpLine: "apt=96"},
			PayloadType:        97,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP9, ClockRate: 90000, SDPFmtpLine: "profile-id=0", RTCPFeedback: videoRTCPFeedback},
			PayloadType:        98,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, SDPFmtpLine: "apt=98"},
			PayloadType:        99,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP9, ClockRate: 90000, SDPFmtpLine: "profile-id=1", RTCPFeedback: videoRTCPFeedback},
			PayloadType:        100,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, SDPFmtpLine: "apt=100"},
			PayloadType:        101,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f", RTCPFeedback: videoRTCPFeedback},
			PayloadType:        102,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, SDPFmtpLine: "apt=102"},
			PayloadType:        121,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=0;profile-level-id=42001f", RTCPFeedback: videoRTCPFeedback},
			PayloadType:        127,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, SDPFmtpLine: "apt=127"},
			PayloadType:        120,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: videoRTCPFeedback},
			PayloadType:        125,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, SDPFmtpLine: "apt=125"},
			PayloadType:        107,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=0;profile-level-id=42e01f", RTCPFeedback: videoRTCPFeedback},
			PayloadType:        108,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, SDPFmtpLine: "apt=108"},
			PayloadType:        109,
		},

		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=0;profile-level-id=42001f", RTCPFeedback: videoRTCPFeedback},
			PayloadType:        127,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, SDPFmtpLine: "apt=127"},
			PayloadType:        120,
		},

		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=640032", RTCPFeedback: videoRTCPFeedback},
			PayloadType:        123,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/rtx", ClockRate: 90000, SDPFmtpLine: "apt=123"},
			PayloadType:        118,
		},

		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: "video/ulpfec", ClockRate: 90000},
			PayloadType:        116,
		},
	} {
		if err := m.RegisterCodec(codec, webrtc.RTPCodecTypeVideo); err != nil {
			return err
		}
	}

	// Default Pion Video Header Extensions
	for _, extension := range []string{
		"urn:ietf:params:rtp-hdrext:sdes:mid",
		"urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id",
		"urn:ietf:params:rtp-hdrext:sdes:repaired-rtp-stream-id",
	} {
		if err := m.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: extension}, webrtc.RTPCodecTypeVideo); err != nil {
			return err
		}
	}

	return nil
}
