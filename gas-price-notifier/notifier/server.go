package notifier

import (
	"fmt"
	"gas-price-notifier/config"
	"gas-price-notifier/data"
	"gas-price-notifier/pubsub"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// Start - Manages whole lifecycle of backend application
func Start() {

	redisClient := pubsub.Connect()
	defer redisClient.Close()

	connCount := data.SafeActiveConnections{
		Lock:        &sync.RWMutex{},
		Connections: &data.ActiveConnections{Count: 0}}

	handle := echo.New()

	handle.Use(middleware.LoggerWithConfig(
		middleware.LoggerConfig{
			Format: "${time_rfc3339} | ${method} | ${uri} | ${status} | ${remote_ip} | ${latency_human}\n",
		}))

	handle.GET("/", func(c echo.Context) error {
		return c.File("assets/index.html")
	})

	handle.GET("/semantic.min.css", func(c echo.Context) error {
		return c.File("assets/semantic.min.css")
	})

	handle.GET("/semantic.min.js", func(c echo.Context) error {
		return c.File("assets/semantic.min.js")
	})

	handle.GET("/themes/default/assets/fonts/icons.woff2", func(c echo.Context) error {
		return c.File("assets/icons.woff2")
	})

	handle.GET("/favicon.ico", func(c echo.Context) error {
		return c.File("assets/favicon.ico")
	})

	handle.GET("/gasz.png", func(c echo.Context) error {
		return c.File("assets/gasz.png")
	})

	handle.GET("/gasz_large.png", func(c echo.Context) error {
		return c.File("assets/gasz_large.png")
	})

	handle.GET("/worker.js", func(c echo.Context) error {
		return c.File("assets/worker.js")
	})

	v1 := handle.Group("/v1")
	upgrader := websocket.Upgrader{}

	{

		v1.GET("/stat", func(c echo.Context) error {

			connCount.Lock.RLock()
			defer connCount.Lock.RUnlock()

			return c.JSON(http.StatusOK, struct {
				Active uint64 `json:"active"`
			}{
				Active: connCount.Connections.Count,
			})
		})

		v1.GET("/subscribe", func(c echo.Context) error {

			conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
			if err != nil {
				log.Printf("[!] Failed to upgrade request : %s\n", err.Error())
				return nil
			}

			// Incrementing number of active connections
			connCount.Increment(1)
			// Decrementing number of active connections
			// because this clientn just got disconnected
			defer connCount.Decrement(1)

			// Scheduling graceful connection closing, to be invoked when
			// getting out of this function's scope
			defer conn.Close()

			// For each client connected over websocket, this associative
			// array to be maintained, so that we can allow each client
			// subscribe tp different price feeds using single connection
			//
			// They will receive notification as soon as any such criteria gets satisfied
			subscriptions := make(map[string]*data.PriceSubscription)
			lock := sync.Mutex{}

			// Unsubscribing from all subscriptions, for this client
			defer func() {
				for _, v := range subscriptions {
					v.Request.Type = "unsubscription"
				}
			}()

			// Handling client request and responding accordingly
			for {

				var payload data.Payload

				// Reading JSON data from client
				if err := conn.ReadJSON(&payload); err != nil {
					log.Printf("[!] Failed to read data from client : %s\n", err.Error())
					// In case, some error is faced, unlocking critical section here
					break
				}

				// Validating client payload
				if err := payload.Validate(); err != nil {

					log.Printf("[!] Invalid payload : %s\n", err.Error())

					// -- Critical section code, starts
					lock.Lock()

					if err := conn.WriteJSON(&data.ClientResponse{
						Code:    0,
						Message: "Bad Subscription Request",
					}); err != nil {
						log.Printf("[!] Failed to communicate with client : %s\n", err.Error())
					}

					lock.Unlock()
					// -- Critical section code, ends

					break

				}

				// Kept so that after control gets out of switch case 👇
				// we can check whether we faced any errors with in switch case or not
				//
				// If yes, we need to get out of this execution loop, which will result in automatic
				// closing of underlying network connection
				var facedErrorInSwitchCase bool

				switch payload.Type {

				case "subscription":

					// Client has already subscribed to this event
					_, ok := subscriptions[payload.String()]
					if ok {
						resp := data.ClientResponse{
							Code:    0,
							Message: "Already Subscribed",
						}

						// -- Critical section code, starts
						lock.Lock()

						if err := conn.WriteJSON(&resp); err != nil {
							facedErrorInSwitchCase = true
							log.Printf("[!] Failed to communicate with client : %s\n", err.Error())
						}

						lock.Unlock()
						// -- Critical section code, ends

						break
					}

					// Creating subscription entry for this client in associative array
					//
					// To be used in future when `unsubscription` request to be received
					subscriptions[payload.String()] = data.NewPriceSubscription(c.Request().Context(), conn, &payload, redisClient, &lock)

				case "unsubscription":

					// Client doesn't have any subscription
					// for this event, so there's no question
					// of unsubscription
					subs, ok := subscriptions[payload.String()]
					if !ok {
						resp := data.ClientResponse{
							Code:    0,
							Message: "Not Subscribed",
						}

						// -- Critical section code, starts
						lock.Lock()

						if err := conn.WriteJSON(&resp); err != nil {
							facedErrorInSwitchCase = true
							log.Printf("[!] Failed to communicate with client : %s\n", err.Error())
						}

						lock.Unlock()
						// -- Critical section code, ends

						break
					}

					// Cancelling subscription
					if subs != nil {
						subs.Request.Type = "unsubscription"
					}

					// Removing subscription entry from associative array
					delete(subscriptions, payload.String())

				}

				// If we've faced any errors in switch case 👆
				// we're just breaking out of loop
				if facedErrorInSwitchCase {
					break
				}

			}

			return nil

		})
	}

	if err := handle.Start(fmt.Sprintf(":%s", config.Get("Port"))); err != nil {
		log.Fatalf("[!] Failed to start server : %s\n", err.Error())
	}

}
