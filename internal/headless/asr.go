package headless

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// MaxAudioBytes is the most audio one transcription takes: about two minutes
// of 16 kHz mono 16-bit PCM, which is longer than anyone talks to glasses.
const MaxAudioBytes = 4 << 20

// WAV wraps raw little-endian 16-bit PCM in a canonical 44-byte RIFF header.
func WAV(pcm []byte, sampleRate, channels int) []byte {
	const bits = 16
	blockAlign := channels * bits / 8
	var b bytes.Buffer
	b.Grow(44 + len(pcm))
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))                    // fmt chunk size
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))                     // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(channels))              //
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate))            //
	_ = binary.Write(&b, binary.LittleEndian, uint32(sampleRate*blockAlign)) // byte rate
	_ = binary.Write(&b, binary.LittleEndian, uint16(blockAlign))            //
	_ = binary.Write(&b, binary.LittleEndian, uint16(bits))                  //
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// TranscribeRequest is one call to the upstream.
type TranscribeRequest struct {
	BaseURL  string
	APIKey   string
	Model    string
	Language string
	Prompt   string
	WAV      []byte
}

// UpstreamError is the upstream refusing, with what it said.
type UpstreamError struct {
	Status  int
	Message string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("speech-to-text upstream answered %d: %s", e.Status, e.Message)
}

// Transcribe posts the audio to <baseURL>/audio/transcriptions, the OpenAI
// shape that SiliconFlow, Groq and a local whisper server all speak, and
// returns the text.
func Transcribe(ctx context.Context, client *http.Client, req TranscribeRequest) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="audio.wav"`)
	h.Set("Content-Type", "audio/wav")
	part, err := mw.CreatePart(h)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(req.WAV); err != nil {
		return "", err
	}
	fields := [][2]string{{"model", req.Model}, {"language", req.Language}, {"prompt", req.Prompt}, {"response_format", "json"}}
	for _, f := range fields {
		if f[1] == "" {
			continue
		}
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return "", err
		}
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(req.BaseURL, "/")+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	hr.Header.Set("Content-Type", mw.FormDataContentType())
	if req.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+req.APIKey)
	}
	res, err := client.Do(hr)
	if err != nil {
		return "", &UpstreamError{Status: 0, Message: err.Error()}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return "", &UpstreamError{Status: res.StatusCode, Message: upstreamMessage(raw)}
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", &UpstreamError{Status: res.StatusCode, Message: "the answer was not JSON with a text field: " + clip(oneLine(string(raw)), 200)}
	}
	return strings.TrimSpace(out.Text), nil
}

// upstreamMessage is the readable part of an error body: OpenAI's
// {"error":{"message"}}, SiliconFlow's {"message"}, or the text itself.
func upstreamMessage(raw []byte) string {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil {
		if e.Message != "" {
			return clip(e.Message, 300)
		}
		var inner struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &inner) == nil && inner.Message != "" {
			return clip(inner.Message, 300)
		}
		var s string
		if json.Unmarshal(e.Error, &s) == nil && s != "" {
			return clip(s, 300)
		}
	}
	msg := clip(oneLine(string(raw)), 300)
	if msg == "" {
		msg = "no message"
	}
	return msg
}
