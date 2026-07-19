package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // dev: allow all origins
}

func registerWS(r *gin.Engine, d Deps) {
	r.GET("/load-tests/:id/stream", func(c *gin.Context) {
		id := c.Param("id")
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		ctx := c.Request.Context()
		metrics, cancel := d.Signal.SubscribeMetrics(ctx, id)
		defer cancel()

		// reader goroutine detects client close
		closed := make(chan struct{})
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					close(closed)
					return
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case <-closed:
				return
			case msg, ok := <-metrics:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			}
		}
	})
}
