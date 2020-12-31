package data

import (
	"context"
	"encoding/json"
	"fmt"
	"gas-price-notifier/config"
	"log"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/websocket"
)

// PriceSubscription - Manages whole life cycle of price feed subscription
// for each client
//
// Functions defined on this struct, are supposed to be invoked for subscribing to and unsubscribing from
// Redis pubsub topic, where price feed data is being published
type PriceSubscription struct {
	Client  *websocket.Conn
	Request *Payload
	Redis   *redis.Client
	PubSub  *redis.PubSub
}

// Subscribe - Subscribing to Redis pubsub channel
// so that any time new price feed is posted in channel
// listener will get notified & take proper measurements
// if conditions satisfy
func (ps *PriceSubscription) Subscribe(ctx context.Context) {
	ps.PubSub = ps.Redis.Subscribe(ctx, config.Get("RedisPubSubChannel"))
}

// Listen - Subscribing to Redis pubsub and waiting for message
// to be published, as soon as it's published it's being sent to
// client application, connected via websocket connection
//
//
func (ps *PriceSubscription) Listen(ctx context.Context) {

	// Scheduling unsubscription call here, to be invoked when
	// returning from this function
	defer ps.Unsubscribe(ctx)

	for {

		if ps.Request.Type != "subscription" {
			break
		}

		msg, err := ps.PubSub.ReceiveTimeout(ctx, time.Second)
		if err != nil {
			continue
		}

		var facedErrorInSwitchCase bool

		switch m := msg.(type) {

		case *redis.Subscription:

			resp := ClientResponse{
				Code:    1,
				Message: fmt.Sprintf("Subscribed to `%s`", ps.Request),
			}

			if err := ps.Client.WriteJSON(&resp); err != nil {
				facedErrorInSwitchCase = true
				log.Printf("[!] Failed to communicate with client : %s\n", err.Error())

				break
			}

		case *redis.Message:

			var pubsubPayload PubSubPayload
			_msg := []byte(m.Payload)

			if err := json.Unmarshal(_msg, &pubsubPayload); err != nil {
				facedErrorInSwitchCase = true
				log.Printf("[!] Failed to decode received data from pubsub channel : %s\n", err.Error())

				break
			}

			if err := ps.Client.WriteJSON(&pubsubPayload); err != nil {
				facedErrorInSwitchCase = true
				log.Printf("[!] Failed to communicate with client : %s\n", err.Error())
			}

		}

		// Checking whether we've encountered any error with in switch case
		//
		// If yes, we can break out of this loop
		if facedErrorInSwitchCase {
			break
		}

	}

}

// Unsubscribe - Cancelling price feed subscription for specific user
// and letting client know about it
func (ps *PriceSubscription) Unsubscribe(ctx context.Context) {

	if err := ps.PubSub.Unsubscribe(ctx, config.Get("RedisPubSubChannel")); err != nil {
		log.Printf("[!] Failed to unsubscribe from pubsub topic : %s\n", err.Error())
		return
	}

	resp := ClientResponse{
		Code:    1,
		Message: fmt.Sprintf("Unsubscribed from `%s`", ps.Request),
	}

	if err := ps.Client.WriteJSON(&resp); err != nil {
		log.Printf("[!] Failed to communicate with client : %s\n", err.Error())
	}

}

// NewPriceSubscription - Creating new price data subscription for client
// connected over websocket
//
// Whether client will receive notification that depends on whether received price value
// satisfies criteria set by client
func NewPriceSubscription(ctx context.Context, client *websocket.Conn, request *Payload, redisClient *redis.Client) *PriceSubscription {

	ps := PriceSubscription{
		Client:  client,
		Request: request,
		Redis:   redisClient,
	}

	// Subscription object to be stored in 👆 struct
	// after calling this function
	ps.Subscribe(ctx)
	// Running listener i.e. subscriber in different execution thread
	go ps.Listen(ctx)

	return &ps

}