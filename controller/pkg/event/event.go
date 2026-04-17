package event

import (
	"log"
	"sync"
)

// EventType 事件类型
type EventType string

const (
	EventMoonOnline   EventType = "moon_online"
	EventMoonOffline  EventType = "moon_offline"
	EventPeerOnline   EventType = "peer_online"
	EventPeerOffline  EventType = "peer_offline"
	EventTunnel       EventType = "tunnel_event"
	EventAlert       EventType = "alert_event"
	EventTraffic     EventType = "traffic_event"
	EventConfigChange EventType = "config_change"
)

// Event 事件结构
type Event struct {
	Type      EventType
	Timestamp int64
	Data      interface{}
}

// Subscriber 事件订阅者
type Subscriber struct {
	EventTypes []EventType
	Handler   func(event *Event)
}

// EventBus 事件总线
type EventBus struct {
	subscribers []Subscriber
	mu         sync.RWMutex
}

var (
	globalBus *EventBus
	once      sync.Once
)

// GetEventBus 获取全局事件总线
func GetEventBus() *EventBus {
	once.Do(func() {
		globalBus = &EventBus{
			subscribers: make([]Subscriber, 0),
		}
	})
	return globalBus
}

// Subscribe 订阅事件
func (bus *EventBus) Subscribe(sub Subscriber) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.subscribers = append(bus.subscribers, sub)
	log.Printf("事件订阅：%v", sub.EventTypes)
}

// Publish 发布事件
func (bus *EventBus) Publish(event *Event) {
	bus.mu.RLock()
	defer bus.mu.RUnlock()

	for _, sub := range bus.subscribers {
		for _, eventType := range sub.EventTypes {
			if eventType == event.Type {
				go sub.Handler(event)
				break
			}
		}
	}
}

// PublishAsync 异步发布事件
func (bus *EventBus) PublishAsync(event *Event) {
	go func() {
		bus.Publish(event)
	}()
}
