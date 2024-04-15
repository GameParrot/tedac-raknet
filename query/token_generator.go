package query

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
)

type tokenGenerator struct {
	token string
}

func (tg *tokenGenerator) generateToken() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func getTokenString(token, salt string) int {
	hash := sha512.New()
	hash.Write([]byte(salt + ":" + token))
	hashed := hash.Sum(nil)
	readInt := binary.BigEndian.Uint32(hashed[7:11])
	return int(readInt)
}
