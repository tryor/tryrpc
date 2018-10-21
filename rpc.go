package tryrpc

import (
	"errors"
	"fmt"
	"sync"

	"log"
	"net"
	"reflect"
	"sync/atomic"
	"time"
)

const (
	REQUEST      = 0
	RESPONSE     = 1
	NOTIFICATION = 2
)

type iConn struct {
	net.Conn
	readWriteError int32
}

func (c *iConn) SetReadWriteError() {
	atomic.StoreInt32(&c.readWriteError, 1)
}

func (c *iConn) IsReadWriteError() bool {
	return atomic.LoadInt32(&c.readWriteError) == 1
}

type rpcConn struct {
	*iConn
	WriteChan chan []byte
	running   int32
	Log       *log.Logger
	chanLock  *sync.RWMutex
}

func newRpcConn(conn net.Conn, _log *log.Logger) *rpcConn {
	return &rpcConn{&iConn{conn, 0}, make(chan []byte), 0, _log, new(sync.RWMutex)}
}

func (c *rpcConn) Start() {
	if atomic.LoadInt32(&c.running) == 1 {
		return
	}
	atomic.StoreInt32(&c.running, 1)

	go func() {
		for {
			//write rpc msgpack
			select {
			case data, ok := <-c.WriteChan:
				if !ok || data == nil || len(data) == 0 {
					goto WriteEnd
				}
				_, err := c.Write(data)
				if err != nil {
					c.SetReadWriteError()
					c.Log.Println(makeLoginfo(c.Conn, err))
					goto WriteEnd
				}
			case <-time.After(5 * time.Second):
			}

			if atomic.LoadInt32(&c.running) == 0 {
				break
			}

		}
	WriteEnd:
		c.closeWriteChan()
	}()
}

func (c *rpcConn) Stop() {
	if atomic.LoadInt32(&c.running) == 0 {
		return
	}
	atomic.StoreInt32(&c.running, 0)
}

func (c *rpcConn) closeWriteChan() {
	c.chanLock.Lock()
	defer c.chanLock.Unlock()
	if c.WriteChan != nil {
		close(c.WriteChan)
		c.WriteChan = nil
	}
}

func (c *rpcConn) Close() error {
	c.Stop()
	c.closeWriteChan()
	return c.Conn.Close()
}

func toInteger(v interface{}) (int, error) {
	switch v_ := v.(type) {
	case int8:
		return int(v_), nil
	case int16:
		return int(v_), nil
	case int32:
		return int(v_), nil
	case int64:
		return int(v_), nil
	case uint8:
		return int(v_), nil
	case uint16:
		return int(v_), nil
	case uint32:
		return int(v_), nil
	case uint64:
		return int(v_), nil
	case int:
		return v_, nil
	}
	return 0, errors.New("Invalid message format")
}

var timeType = reflect.TypeOf(time.Now())

func toReflectValues(dVals []reflect.Value, sVals []interface{}, dTypes []reflect.Type) error {
	for i := 0; i < len(sVals); i++ {
		rv, err := toReflectValue(sVals[i], dTypes[i])
		if err != nil {
			return err
		} else {
			dVals[i] = *rv
		}
	}
	return nil
}

func toReflectValue(v interface{}, dType reflect.Type) (*reflect.Value, error) {
	reftV := reflect.ValueOf(v)
	switch dType.Kind() {
	case reflect.Slice:
		vp := reflect.Indirect(reflect.New(dType))
		for i := 0; i < reftV.Len(); i++ {
			sv := reftV.Index(i)
			subVp, err := toReflectValue(sv.Interface(), dType.Elem())
			if err != nil {
				return nil, err
			}
			vp = reflect.Append(vp, *subVp)
		}
		return &vp, nil
	case reflect.Map:
		vp := reflect.MakeMap(dType)
		for _, k := range reftV.MapKeys() {
			sv := reftV.MapIndex(k)
			subKp, err := toReflectValue(k.Interface(), dType.Key())
			if err != nil {
				return nil, err
			}
			subVp, err := toReflectValue(sv.Interface(), dType.Elem())
			if err != nil {
				return nil, err
			}
			vp.SetMapIndex(*subKp, *subVp)

		}
		return &vp, nil

	case reflect.Struct:
		if dType != timeType {
			return nil, errors.New("The parameter type is not supported, type is Struct")
		}
	}
	return &reftV, nil
}

func parseRPCData(data interface{}) (msgType int, msgId int, funcName string, arguments []interface{}, err error) {
	for {
		_data, ok := data.([]interface{})
		if !ok {
			break
		}

		dataLen := len(_data)
		if dataLen != 3 && dataLen != 4 {
			break
		}
		msgType, err = toInteger(_data[0])
		if err != nil {
			break
		}

		if msgType == NOTIFICATION && dataLen != 3 {
			break
		}
		if msgType == REQUEST && dataLen != 4 {
			break
		}

		var _funcName interface{}
		var _arguments interface{}
		if msgType == REQUEST {
			msgId, err = toInteger(_data[1])
			if err != nil {
				break
			}
			_funcName = _data[2]
			_arguments = _data[3]
		} else if msgType == NOTIFICATION {
			_funcName = _data[1]
			_arguments = _data[2]
		} else {
			break
		}

		if funcName, ok = _funcName.(string); !ok {
			break
		}
		if arguments, ok = _arguments.([]interface{}); !ok {
			break
		}
		return
	}
	if err == nil {
		err = errors.New("Invalid message format")
	}
	return
}

func makeLoginfo(conn net.Conn, args ...interface{}) string {
	return fmt.Sprintf("\"%s\"<->\"%s\": %s", conn.LocalAddr(), conn.RemoteAddr(), fmt.Sprint(args...))
}
