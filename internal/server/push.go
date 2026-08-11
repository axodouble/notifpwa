package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// action is one notification button: a label and the URL to open on click.
type action struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// pushPayload is the JSON the service worker receives and turns into a
// notification.
type pushPayload struct {
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	URL     string   `json:"url,omitempty"`
	Tag     string   `json:"tag,omitempty"`
	Image   string   `json:"image,omitempty"`
	Actions []action `json:"actions,omitempty"`
	Urgency string   `json:"urgency,omitempty"`
}

// sendResult summarizes a broadcast.
type sendResult struct {
	Sent   int `json:"sent"`
	Failed int `json:"failed"`
	Pruned int `json:"pruned"`
}

// sendOne is the actual push delivery call. It is a package variable so tests
// can stub it out without hitting a real push service.
var sendOne = func(msg []byte, sub *webpush.Subscription, opts *webpush.Options) (*http.Response, error) {
	return webpush.SendNotification(msg, sub, opts)
}

// sendToSubs delivers p to the given subscriptions, pruning endpoints that
// report they are gone (404/410) and counting sent/failed/pruned.
func (s *Server) sendToSubs(subs []subscription, p pushPayload) (sendResult, error) {
	msg, err := json.Marshal(p)
	if err != nil {
		return sendResult{}, err
	}
	opts := &webpush.Options{
		Subscriber:      s.subscriber,
		VAPIDPublicKey:  s.vapidPub,
		VAPIDPrivateKey: s.vapidPriv,
		TTL:             86400,
		Urgency:         webpush.Urgency(p.Urgency),
	}
	var res sendResult
	for _, sub := range subs {
		wp := &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys:     webpush.Keys{P256dh: sub.Keys.P256dh, Auth: sub.Keys.Auth},
		}
		resp, err := sendOne(msg, wp, opts)
		if err != nil {
			log.Printf("push: send to %s errored: %v", sub.Endpoint, err)
			res.Failed++
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
			// Expire rather than delete. Deleting cascades the device's room
			// memberships away, and iOS revokes subscriptions routinely — the
			// user would have to rejoin every room after re-enabling.
			_ = s.store.expireSubscription(sub.Endpoint)
			res.Pruned++
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			res.Sent++
		default:
			log.Printf("push: send to %s got HTTP %d: %s", sub.Endpoint, resp.StatusCode, body)
			res.Failed++
		}
	}
	return res, nil
}
