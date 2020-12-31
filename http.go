package camweb

import (
	"encoding/base64"
	"fmt"
	"github.com/deepch/vdk/av"
	"github.com/deepch/vdk/codec/h264parser"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"log"
	"net/http"
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
	router.GET("/codec/:uuid", func(c *gin.Context) {
		if streams, ok := config.Streams[c.Param("uuid")]; ok {
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
			if err := control(url, cmd.Cmd, cmd.Dir); err != nil {
				c.AbortWithStatusJSON(500, err.Error())
				return
			}
			c.JSON(200, "OK")
		} else {
			c.JSON(404, "No codecs found")
		}
	})
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

	defer ws.Close()
	var wsWriteLock sync.Mutex

	type Payload struct {
		SUUID string `json:"suuid"`
		Data  string `json:"data"`
	}
	var payload Payload
	if err = ws.ReadJSON(&payload); err != nil {
		log.Println(err)
		return
	}

	suuid := payload.SUUID

	settings, ok := config.Streams[suuid]
	if !ok {
		log.Println("stream not found")
		return
	}

	sd, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		log.Println("DecodeString error", err)
		return
	}

	mediaEngine := &webrtc.MediaEngine{}
	if err = mediaEngine.RegisterDefaultCodecs(); err != nil {
		log.Println("RegisterDefaultCodecs error", err)
		return
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))

	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Println("NewPeerConnection error", err)
		return
	}

	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return
		}
		wsWriteLock.Lock()
		defer wsWriteLock.Unlock()
		if err := ws.WriteJSON(candidate.ToJSON()); err != nil {
			log.Println("Failed sending ICE candidate", err)
		} else {
			log.Println("Sent ICE candidate", candidate.ToJSON().Candidate)
		}
	})

	videoTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", suuid+"_pion")
	if err != nil {
		log.Println("Failed to create video track", err)
		return
	}
	_, err = peerConnection.AddTransceiverFromTrack(videoTrack,
		webrtc.RTPTransceiverInit{
			Direction: webrtc.RTPTransceiverDirectionSendonly,
		},
	)
	if err != nil {
		log.Println("AddTransceiverFromTrack error", err)
		return
	}
	_, err = peerConnection.AddTrack(videoTrack)
	if err != nil {
		log.Println("AddTrack error", err)
		return
	}

	var audioTrack *webrtc.TrackLocalStaticSample
	codecs := settings.Codecs
	if len(codecs) > 1 && (codecs[1].Type() == av.PCM_ALAW || codecs[1].Type() == av.PCM_MULAW) {
		switch codecs[1].Type() {
		case av.PCM_ALAW:
			audioTrack, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA}, "audio", suuid+"audio")
		case av.PCM_MULAW:
			audioTrack, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU}, "audio", suuid+"audio")
		}
		if err != nil {
			log.Println(err)
			return
		}
		_, err = peerConnection.AddTransceiverFromTrack(audioTrack,
			webrtc.RTPTransceiverInit{
				Direction: webrtc.RTPTransceiverDirectionSendonly,
			},
		)
		if err != nil {
			log.Println("AddTransceiverFromTrack error", err)
			return
		}
		_, err = peerConnection.AddTrack(audioTrack)
		if err != nil {
			log.Println(err)
			return
		}
	}

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(sd),
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

	go func() {
		for {
			var candidate webrtc.ICECandidateInit
			if err = ws.ReadJSON(&candidate); err != nil {
				log.Println("Error reading candidate", err, candidate)
				return
			}
			if err = peerConnection.AddICECandidate(candidate); err != nil {
				log.Println("Failed adding ICE Candidate", err, candidate)
				return
			} else {
				log.Println("Set remote ICE candidate", candidate.Candidate)
			}
		}
	}()

	peerConnection.OnICEConnectionStateChange(OnICEConnectionStateChange(peerConnection, suuid, videoTrack, audioTrack))

	wsWriteLock.Lock()
	if err = ws.WriteJSON(map[string]string{
		"sdp": peerConnection.LocalDescription().SDP,
	}); err != nil {
		log.Println("Failed sending SDP back", err)
		return
	} else {
		log.Println("Sent SDP")
	}
	wsWriteLock.Unlock()

	time.Sleep(time.Minute)
}

func OnICEConnectionStateChange(pc *webrtc.PeerConnection, id string, videoTrack, audioTrack *webrtc.TrackLocalStaticSample) func(state webrtc.ICEConnectionState) {
	control := make(chan bool, 10)
	settings, ok := config.Streams[id]
	if !ok {
		return nil
	}
	codec := settings.Codecs[0].(h264parser.CodecData)
	sps := codec.SPS()
	pps := codec.PPS()
	once := sync.Once{}

	return func(connectionState webrtc.ICEConnectionState) {
		log.Println("OnICEConnectionStateChange", connectionState.String())
		if connectionState != webrtc.ICEConnectionStateConnected {
			once.Do(func() {
				err := pc.Close()
				if err != nil {
					log.Println("peerConnection Close error", err)
				}
				close(control)
			})
			return
		}

		cuuid, ch := config.connect(id)
		log.Println("start stream", id, "client", cuuid)
		defer func() {
			log.Println("stop stream", id, "client", cuuid)
			defer config.disconnect(id, cuuid)
		}()
		var start bool
		var vpre, apre time.Duration
		for {
			select {
			case <-control:
				log.Println("Control closed")
				return
			case pck := <-ch:
				if pck.IsKeyFrame {
					start = true
				}
				if !start {
					continue
				}
				if pck.IsKeyFrame {
					pck.Data = append([]byte("\000\000\001"+string(sps)+"\000\000\001"+string(pps)+"\000\000\001"), pck.Data[4:]...)

				} else {
					pck.Data = pck.Data[4:]
				}
				if pck.Idx == 0 && videoTrack != nil {
					err := videoTrack.WriteSample(media.Sample{Data: pck.Data, Duration: pck.Time - vpre})
					if err != nil {
						log.Println("Failed to write video sample", err)
						return
					}
					vpre = pck.Time
				} else if pck.Idx == 1 && audioTrack != nil {
					// the audio is choppy for me unless I trim off 500 microseconds?!
					err := audioTrack.WriteSample(media.Sample{Data: pck.Data, Duration: pck.Time - apre - 500*time.Microsecond})
					if err != nil {
						log.Println("Failed to write audio sample", err)
						return
					}
					apre = pck.Time
					_ = apre
				}
			}
		}
	}
}
