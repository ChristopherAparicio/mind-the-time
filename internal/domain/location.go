package domain

import (
	"sync"
	"time"
)

// The Île-de-France network publishes every schedule in Paris local time,
// whatever the server's own timezone is.
var networkLocation = sync.OnceValues(func() (*time.Location, error) {
	return time.LoadLocation("Europe/Paris")
})

func NetworkLocation() (*time.Location, error) {
	return networkLocation()
}
