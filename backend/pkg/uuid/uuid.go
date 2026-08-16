// Package uuid — генерация UUIDv4 без внешних зависимостей.
package uuid

import (
	"crypto/rand"
	"fmt"
)

// New возвращает случайный UUIDv4 в канонической строковой форме.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand не может отказать на поддерживаемых платформах;
		// если отказал — это фатальная проблема среды.
		panic(fmt.Sprintf("uuid: crypto/rand failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
