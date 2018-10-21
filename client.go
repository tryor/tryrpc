package tryrpc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/silenceper/pool"
	"github.com/vmihailenco/msgpack"
)

var ReconnectInterval time.Duration = time.Second * 1

type Client struct {
	pool      pool.Pool
	nextId    uint32
	connConfs []*connConf
	timeout   []time.Duration //[0]-Connection timeout，[1]-Read timeout，[2]-Write timeout
}

type connConf struct {
	network     string
	addr        string
	connErr     error //bool
	connErrTime time.Time
	lock        sync.Mutex
}

func (cf *connConf) String() string {
	return fmt.Sprintf("%s, %s, %v, %v", cf.network, cf.addr, cf.connErr, cf.connErrTime)
}

//network    - Must be "tcp", "tcp4", "tcp6", "unix" or "unixpacket".
//addrs      - RPC server address list, for example: 127.0.0.1:50000;192.168.254.133:50000, use ";" separated
//initialCap - Initial connection pool size
//maxCap     - Maximum number of connection pool connections
//timeout    - [0]-Connection timeout，[1]-Read timeout，[2]-Write timeout, [3] - Connection idle timeout
//                 If the parameter value is 0, it is ignored
func NewClient(network, addrs string, initialCap, maxCap int, timeout ...time.Duration) (*Client, error) {
	_addrs := strings.Split(addrs, ";")
	connConfs := make([]*connConf, len(_addrs))
	for i, addr := range _addrs {
		connConfs[i] = &connConf{network: network, addr: addr, connErrTime: time.Now()}
	}
	c := &Client{nextId: 1, connConfs: connConfs, timeout: timeout}
	factory := func() (conn interface{}, err error) {

		defer func() {
			if err1 := recover(); err1 != nil {
				var ok bool
				if err, ok = err1.(error); !ok {
					err = errors.New(fmt.Sprint(err1))
				}
			}
		}()

		connFlags := make(map[string]*connConf)
		for {
			cf := c.connConfs[rand.Intn(len(c.connConfs))]
			conn, err = c.connect(cf)
			if err != nil {
				connFlags[cf.addr] = cf
				if len(connFlags) >= len(_addrs) {
					break
				}
				continue
			}
			break
		}
		return
	}
	close := func(v interface{}) error {
		return v.(*iConn).Close()
	}

	var idleTimeout time.Duration
	if len(timeout) > 3 {
		idleTimeout = timeout[3]
	}

	poolConfig := &pool.PoolConfig{
		InitialCap: 1,
		MaxCap:     10,
		Factory:    factory,
		Close:      close,
		//Ping:       ping,
		IdleTimeout: idleTimeout,
	}
	if initialCap > poolConfig.InitialCap {
		poolConfig.InitialCap = initialCap
	}
	if maxCap > poolConfig.MaxCap {
		poolConfig.MaxCap = maxCap
	}
	p, err := pool.NewChannelPool(poolConfig)
	if err != nil {
		return nil, err
	}
	c.pool = p
	return c, nil
}

func (cf *connConf) connectError(err error) {
	cf.lock.Lock()
	defer cf.lock.Unlock()
	if err != nil {
		cf.connErrTime = time.Now()
		cf.connErr = err
	} else {
		cf.connErr = nil
	}

}

func (cf *connConf) connectEnable() (err error) {
	cf.lock.Lock()
	defer cf.lock.Unlock()
	if cf.connErr != nil && time.Now().Sub(cf.connErrTime) < ReconnectInterval {
		err = cf.connErr //errors.New("Connection is disconnected, addr is " + cf.addr)
	}
	if err == nil && cf.connErr != nil {
		cf.connErr = nil
	}
	return
}

func (this *Client) setReadDeadline(conn *iConn) {
	if len(this.timeout) > 1 && this.timeout[1] > 0 {
		conn.SetReadDeadline(time.Now().Add(this.timeout[1]))
	}
}

func (this *Client) setWriteDeadline(conn *iConn) {
	if len(this.timeout) > 2 && this.timeout[2] > 0 {
		conn.SetWriteDeadline(time.Now().Add(this.timeout[2]))
	}
}

func (this *Client) connect(connConf *connConf) (conn *iConn, err error) {
	err = connConf.connectEnable()
	if err != nil {
		return
	}
	var c net.Conn
	if len(this.timeout) <= 0 {
		c, err = net.Dial(connConf.network, connConf.addr)
	} else {
		c, err = net.DialTimeout(connConf.network, connConf.addr, this.timeout[0])
	}

	if err == nil {
		conn = &iConn{Conn: c}
	} else {
		connConf.connectError(err)
	}

	return
}

func (this *Client) Close() {
	this.pool.Release()
}

//Send notification, no return value
func (this *Client) Send(funcName string, arguments ...interface{}) (err error) {
	var conn interface{}
	conn, err = this.pool.Get()
	if err != nil {
		return
	}
	defer func() {
		if err1 := recover(); err1 != nil {
			var ok bool
			if err, ok = err1.(error); !ok {
				err = errors.New(fmt.Sprint(err1))
			}
			this.pool.Close(conn)
		} else {
			if conn.(*iConn).IsReadWriteError() {
				this.pool.Close(conn)
			} else {
				this.pool.Put(conn)
			}
		}
	}()

	var buf bytes.Buffer
	buf.WriteByte(0x93)
	enc := msgpack.NewEncoder(&buf)
	err = enc.EncodeMulti(uint8(NOTIFICATION), funcName, arguments)
	if err != nil {
		return
	}

	this.setWriteDeadline(conn.(*iConn))
	_, err = conn.(io.Writer).Write(buf.Bytes())
	if err != nil {
		conn.(*iConn).SetReadWriteError()
		return
	}
	return nil
}

//A remote method call with a return value
func (this *Client) Call(funcName string, arguments ...interface{}) (result interface{}, err error) {
	var conn interface{}
	conn, err = this.pool.Get()
	if err != nil {
		return
	}
	defer func() {
		if err1 := recover(); err1 != nil {
			var ok bool
			if err, ok = err1.(error); !ok {
				err = errors.New(fmt.Sprint(err1))
			}
			this.pool.Close(conn)
		} else {
			if conn.(*iConn).IsReadWriteError() {
				this.pool.Close(conn)
			} else {
				this.pool.Put(conn)
			}
		}
	}()

	var msgId = atomic.LoadUint32(&this.nextId)
	atomic.AddUint32(&this.nextId, 1)
	err = this.request(conn.(io.Writer), msgId, funcName, arguments...)
	if err != nil {
		return
	}
	var _msgId int
	_msgId, result, err = this.response(conn.(io.Reader))
	if err != nil {
		return
	}
	if msgId != uint32(_msgId) {
		err = errors.New(fmt.Sprintf("Message IDs don't match (%d != %d)", msgId, _msgId))
		return
	}
	return result, nil
}

func (this *Client) request(writer io.Writer, msgId uint32, funcName string, arguments ...interface{}) (err error) {
	var buf bytes.Buffer
	buf.WriteByte(0x94)
	enc := msgpack.NewEncoder(&buf)
	err = enc.EncodeMulti(uint8(REQUEST), msgId, funcName, arguments)
	if err != nil {
		return
	}

	this.setWriteDeadline(writer.(*iConn))
	_, err = writer.Write(buf.Bytes())
	if err != nil {
		writer.(*iConn).SetReadWriteError()
		return
	}
	return nil
}

func (this *Client) response(reader io.Reader) (msgId int, retVal interface{}, err error) {
	var data interface{}
	var msgType int
	dec := msgpack.NewDecoder(reader)

	this.setReadDeadline(reader.(*iConn))
	data, err = dec.DecodeInterface()
	if err != nil {
		reader.(*iConn).SetReadWriteError()
		return 0, nil, err
	}
	for {
		_data, ok := data.([]interface{})
		if !ok {
			break
		}
		if len(_data) != 4 {
			break
		}
		msgType, err = toInteger(_data[0])
		if err != nil {
			break
		}
		if msgType != RESPONSE {
			break
		}
		msgId, err = toInteger(_data[1])
		if err != nil {
			break
		}
		errMsg := _data[2]
		if errMsg != nil {
			if _errMsg, ok := errMsg.(string); ok {
				err = errors.New(_errMsg)
			} else {
				err = errors.New(fmt.Sprint(errMsg))
			}
			break
		}
		return msgId, _data[3], nil
	}
	if err == nil {
		err = errors.New("Invalid message format")
	}
	return msgId, nil, err
}
