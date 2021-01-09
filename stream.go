package camweb

import (
	"bytes"
	"github.com/deepch/vdk/av"
	"github.com/deepch/vdk/codec/h264parser"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"log"
	"time"

	"github.com/deepch/vdk/format/rtsp"
)

func ServeStreams() {
	for k, v := range config.Streams {
		go stream(k, v.URL)
	}
}

var annexbNALUStartCode = []byte{0x00, 0x00, 0x00, 0x01}

// stream connects to the camera and starts sending it to any connected clients
func stream(name, url string) {
	for {
		//rtsp.DebugRtsp = true
		session, err := rtsp.Dial(url)
		if err != nil {
			log.Println(name, err)
			time.Sleep(5 * time.Second)
			continue
		}
		session.RtpKeepAliveTimeout = 10 * time.Second
		if err != nil {
			log.Println(name, err)
			time.Sleep(5 * time.Second)
			continue
		}
		codecs, err := session.Streams()
		if err != nil {
			log.Println(name, err)
			time.Sleep(5 * time.Second)
			continue
		}

		videoTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", name+"_pion")
		if err != nil {
			log.Println("Failed to create video track", err)
			return
		}

		var audioTrack *webrtc.TrackLocalStaticSample
		if len(codecs) > 1 && (codecs[1].Type() == av.PCM_ALAW || codecs[1].Type() == av.PCM_MULAW) {
			switch codecs[1].Type() {
			case av.PCM_ALAW:
				audioTrack, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA}, "audio", name+"audio")
			case av.PCM_MULAW:
				audioTrack, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU}, "audio", name+"audio")
			}
			if err != nil {
				log.Println(err)
				return
			}
		}

		config.setStream(name, codecs, videoTrack, audioTrack)

		var videoPrevious time.Duration
		for {
			pck, err := session.ReadPacket()
			if err != nil {
				log.Println("Failed reading packet on stream", name, err)
				break
			}
			if pck.Idx == 0 && videoTrack != nil {
				if pck.IsKeyFrame {
					// SPS and PPS may change
					codecs, err = session.Streams()
					if err != nil {
						break
					}
					codec := codecs[0].(h264parser.CodecData)
					var keyframePreamble bytes.Buffer
					keyframePreamble.Write(annexbNALUStartCode)
					keyframePreamble.Write(codec.SPS())
					keyframePreamble.Write(annexbNALUStartCode)
					keyframePreamble.Write(codec.PPS())
					keyframePreamble.Write(annexbNALUStartCode)

					pck.Data = append(keyframePreamble.Bytes(), pck.Data[4:]...)
				} else {
					pck.Data = pck.Data[4:]
				}

				err = videoTrack.WriteSample(media.Sample{Data: pck.Data, Duration: pck.Time - videoPrevious})
				if err != nil {
					log.Println("Failed to write video sample", err)
					break
				}
				videoPrevious = pck.Time
			} else if pck.Idx == 1 && audioTrack != nil {
				codecs, err = session.Streams()
				if err != nil {
					break
				}
				codec := codecs[1].(av.AudioCodecData)
				duration, err := codec.PacketDuration(pck.Data)
				if err != nil {
					log.Println("Failed to get duration for audio:", err)
					break
				}
				err = audioTrack.WriteSample(media.Sample{Data: pck.Data, Duration: duration})
				if err != nil {
					log.Println("Failed to write audio sample", err)
					break
				}
			}
		}
		if err = session.Teardown(); err != nil {
			log.Println("teardown error", err)
		}
		if err = session.Close(); err != nil {
			log.Println("session Close error", err)
		}
		log.Println(name, "reconnect wait 5s")
		time.Sleep(5 * time.Second)
	}
}
