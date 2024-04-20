package query

import (
	"bytes"
	"errors"
	"net"
	"sync"
)

type QueryHandler struct {
	queryInfo    map[string]string
	queryInfoMut sync.Mutex

	players    []string
	playersMut sync.Mutex

	tokenGen tokenGenerator
}

func New(queryInfo map[string]string, players []string) *QueryHandler {
	query := &QueryHandler{queryInfo: queryInfo, tokenGen: tokenGenerator{}, players: players}
	query.tokenGen.generateToken()
	return query
}

func (q *QueryHandler) SetQueryInfo(queryInfo map[string]string) {
	q.queryInfoMut.Lock()
	q.queryInfo = queryInfo
	q.queryInfoMut.Unlock()
}

func (q *QueryHandler) SetPlayers(players []string) {
	q.playersMut.Lock()
	q.players = players
	q.playersMut.Unlock()
}

func (q *QueryHandler) HandlePacket(b *bytes.Buffer, addr net.Addr) error {
	pk := request{}
	if err := pk.Unmarshal(b); err != nil {
		return err
	}

	var resp response = response{}
	switch pk.RequestType {
	case queryTypeHandshake:
		resp = response{ResponseType: queryTypeHandshake, SequenceNumber: pk.SequenceNumber, ResponseNumber: int32(getTokenString(q.tokenGen.token, addr.String()))}
	case queryTypeInformation:
		if pk.ResponseNumber != int32(getTokenString(q.tokenGen.token, addr.String())) {
			return errors.New("token mismatch")
		}
		q.queryInfoMut.Lock()
		q.playersMut.Lock()
		resp = response{ResponseType: queryTypeInformation, SequenceNumber: pk.SequenceNumber, ResponseNumber: pk.ResponseNumber, Information: q.queryInfo, Players: q.players}
		q.queryInfoMut.Unlock()
		q.playersMut.Unlock()

	}
	resp.Marshal(b)
	return nil
}
