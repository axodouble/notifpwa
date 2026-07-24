package server

import (
	"encoding/json"
	"net/http"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// pushPayload is the JSON the service worker receives and turns into a
// notification.
type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
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

// broadcast sends the payload to every stored device. Endpoints that report
// they are gone (404/410) are pruned from the database.
func (s *Server) broadcast(p pushPayload) (sendResult, error) {
	subs, err := s.store.listSubscriptions()
	if err != nil {
		return sendResult{}, err
	}

	msg, err := json.Marshal(p)
	if err != nil {
		return sendResult{}, err
	}

	opts := &webpush.Options{
		Subscriber:      s.subscriber,
		VAPIDPublicKey:  s.vapidPub,
		VAPIDPrivateKey: s.vapidPriv,
		TTL:             86400,
	}

	var res sendResult
	for _, sub := range subs {
		wp := &webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys:     webpush.Keys{P256dh: sub.Keys.P256dh, Auth: sub.Keys.Auth},
		}
		resp, err := sendOne(msg, wp, opts)
		if err != nil {
			res.Failed++
			continue
		}
		resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
			_ = s.store.deleteSubscription(sub.Endpoint)
			res.Pruned++
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			res.Sent++
		default:
			res.Failed++
		}
	}
	return res, nil
}
