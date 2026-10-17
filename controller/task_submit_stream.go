package controller

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// taskSubmitStream presents synchronous image events. Partial results are live;
// the terminal event is held until the task and its billing are durable.
type taskSubmitStream struct {
	c                       *gin.Context
	client                  context.Context
	terminalEvent, terminal string
	started                 bool
}

func beginTaskSubmitStream(c *gin.Context, enabled bool) (*taskSubmitStream, func()) {
	if !enabled || c.Request.Context().Err() != nil {
		return nil, func() {}
	}
	request := c.Request
	stream := &taskSubmitStream{c: c, client: request.Context()}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), defaultPluginProtocolBridgeDeps().submissionTimeout)
	write := relaycommon.TaskSubmitEventWriter(func(event string, data []byte, terminal bool) {
		if terminal {
			stream.terminalEvent, stream.terminal = event, string(data)
		} else {
			stream.write(event, string(data))
		}
	})
	c.Request = request.Clone(context.WithValue(ctx, relaycommon.TaskSubmitEventWriterKey{}, write))
	return stream, func() { c.Request = request; cancel() }
}

func (s *taskSubmitStream) write(event, data string) {
	if s.client.Err() != nil {
		return
	}
	s.c.Header("Content-Type", "text/event-stream")
	s.c.Header("Cache-Control", "no-cache")
	s.c.Header("X-Accel-Buffering", "no")
	s.started = true
	_, _ = fmt.Fprintf(s.c.Writer, "event: %s\ndata: %s\n\n", event, strings.ReplaceAll(data, "\n", "\ndata: "))
	s.c.Writer.Flush()
}

func (s *taskSubmitStream) finish() bool {
	if s == nil || s.terminal == "" {
		return false
	}
	s.write(s.terminalEvent, s.terminal)
	return true
}

func (s *taskSubmitStream) writeError(taskErr *dto.TaskError) bool {
	if s == nil || !s.started {
		return false
	}
	payload, _ := common.Marshal(gin.H{"type": "error", "error": gin.H{"code": taskErr.Code, "message": http.StatusText(http.StatusBadGateway)}})
	s.write("error", string(payload))
	return true
}
