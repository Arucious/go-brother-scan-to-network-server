package brother

import (
	"context"
	"errors"
	"log/slog"
	"net"
)

// EventHandler receives decoded button events.
type EventHandler func(ev Event, from *net.UDPAddr)

// Listen receives button events on addr (e.g. ":54925") until ctx is done.
// Every valid event is acknowledged by echoing the datagram back to the
// device, exactly like NETserver does.
func Listen(ctx context.Context, addr string, log *slog.Logger, handle EventHandler) error {
	conn, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	buf := make([]byte, 1500)
	for {
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil //nolint:nilerr // closed by shutdown, not a failure
			}
			log.Warn("udp read failed", "err", err)
			continue
		}
		from, _ := src.(*net.UDPAddr)
		packet := append([]byte(nil), buf[:n]...)

		ev, ok, perr := ParseEventPacket(packet)
		if !ok {
			log.Debug("ignoring non scan-key datagram", "from", src, "bytes", n, "err", perr)
			continue
		}
		if _, err := conn.WriteTo(packet, src); err != nil {
			log.Warn("failed to acknowledge event", "from", src, "err", err)
		}
		if perr != nil {
			log.Warn("malformed scan-key event", "from", src, "payload", string(packet[4:]), "err", perr)
			continue
		}
		log.Info("scan button pressed", "from", src, "user", ev.User, "function", ev.Function, "payload", ev.Info.Encode())
		handle(ev, from)
	}
}
