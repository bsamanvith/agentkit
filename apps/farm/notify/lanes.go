package notify

import (
	"context"
	"log/slog"
)

// Log is a lane that writes each message to log: for trying the tree on a
// laptop with no keys. It needs no address or consent.
func Log(log *slog.Logger) Lane { return logLane{log} }

type logLane struct{ log *slog.Logger }

func (logLane) Info() LaneInfo {
	return LaneInfo{Name: "log", Address: AddrNone, Note: "Writes messages to the server's log, for testing."}
}

func (l logLane) Deliver(_ context.Context, m Message) (Receipt, error) {
	l.log.Info("notify: message", "to", m.Recipient.ID, "name", m.Recipient.Name, "severity", m.Severity, "title", m.Title, "body", m.Body)
	return Receipt{Status: StatusDelivered}, nil
}
