package tryrpc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"reflect"
	"time"

	"github.com/vmihailenco/msgpack"
)

type Server struct {
	Log       *log.Logger
	handlers  map[string]*handler
	listeners []net.Listener
	lchan     chan int
	Mux       bool
}

type handler struct {
	name     string         //function name
	f        reflect.Value  //function
	fType    reflect.Type   //function type
	fInTypes []reflect.Type //function in param type
	numIn    int
	modeOut  int //0, 1, 2, 3
}

const (
	modeOutNo        = 0
	modeOutVal       = 1
	modeOutErr       = 2
	modeOutValAndErr = 3
)

func NewServer() *Server {
	return &Server{
		Log:       log.New(os.Stderr, "msgpack: ", log.Ldate|log.Ltime|log.Lshortfile),
		handlers:  make(map[string]*handler),
		listeners: make([]net.Listener, 0),
		Mux:       true,
	}
}

func (s *Server) Listen(l ...net.Listener) {
	s.listeners = append(s.listeners, l...)
}

func (s *Server) Run() {
	lchan := make(chan int)
	for _, l := range s.listeners {
		go s.accept(l)
	}
	s.lchan = lchan
	<-lchan
	for _, l := range s.listeners {
		l.Close()
	}
}

func (s *Server) ListenAndRun(l ...net.Listener) {
	s.Listen(l...)
	s.Run()
}

func (s *Server) Stop() {
	if s.lchan != nil {
		lchan := s.lchan
		s.lchan = nil
		lchan <- 1
	}
}

func (s *Server) isStoped() bool {
	return s.lchan == nil
}

func (s *Server) Bind(name string, f interface{}) {
	_f := reflect.ValueOf(f)
	fType := _f.Type()

	fInTypes := make([]reflect.Type, fType.NumIn())
	for i := 0; i < fType.NumIn(); i++ {
		fInTypes[i] = fType.In(i)
	}

	var modeOut int
	switch fType.NumOut() {
	case 0:
		modeOut = modeOutNo
	case 1:
		if fType.Out(0).Name() != "error" {
			modeOut = modeOutVal
		} else {
			modeOut = modeOutErr
		}
	case 2:
		modeOut = modeOutValAndErr
		if fType.Out(1).Name() != "error" {
			panic(`The second parameter must be an "error" type, func name is ` + name)
		}
	default:
		panic("The number of return values must be 0 or 1 or 2, func name is " + name)
	}
	s.handlers[name] = &handler{
		name, _f, fType, fInTypes, fType.NumIn(), modeOut}
}

func (s *Server) accept(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			if s.isStoped() {
				break
			}
			s.Log.Println(err)
			continue
		}
		if s.isStoped() {
			conn.Close()
			break
		}
		if s.Mux {
			rconn := newRpcConn(conn, s.Log)
			rconn.Start()
			go s.handleMuxReq(rconn)
		} else {
			go s.handleReq(conn)
		}
	}
}

func (s *Server) handleMuxReq(rconn *rpcConn) {
	defer func() {
		if err := recover(); err != nil {
			s.Log.Println(makeLoginfo(rconn, "Handle rpc request error, cause:", err))
		}
		rconn.Close()
	}()
	dec := msgpack.NewDecoder(rconn)
	for {
		data, err := dec.DecodeInterface()
		if err == io.EOF {
			break
		} else if err != nil {
			s.Log.Println(makeLoginfo(rconn, err))
			break
		}
		go func() {
			defer func() {
				if err := recover(); err != nil {
					s.Log.Println(makeLoginfo(rconn, "Handle rpc request error, cause:", err))
					rconn.Close()
				}
			}()
			s.handleMsg(rconn, data)
			if s.isStoped() {
				rconn.Close()
			}
		}()
	}
	rconn.Close()
}

func (s *Server) handleReq(conn net.Conn) {
	defer func() {
		if err := recover(); err != nil {
			s.Log.Println(makeLoginfo(conn, "Handle rpc request error, cause:", err))
		}
		conn.Close()
	}()
	dec := msgpack.NewDecoder(conn)
	for {
		data, err := dec.DecodeInterface()
		if err == io.EOF {
			break
		} else if err != nil {
			s.Log.Println(makeLoginfo(conn, err))
			break
		}
		s.handleMsg(conn, data)
		if s.isStoped() {
			break
		}
	}
}

func (s *Server) handleMsg(conn net.Conn, data interface{}) {
	msgType, msgId, funcName, arguments, err := parseRPCData(data)
	if err != nil {
		s.Log.Println(makeLoginfo(conn, err))
		if msgType == REQUEST {
			s.sendErrorResponseMessage(conn, msgId, err.Error())
		}
		return
	}

	h, ok := s.handlers[funcName]
	if !ok {
		errMsg := "The handler was not found, name is " + funcName
		s.Log.Println(makeLoginfo(conn, errMsg))
		if msgType == REQUEST {
			s.sendErrorResponseMessage(conn, msgId, errMsg)
		}
		return
	}

	if h.numIn != len(arguments) {
		errMsg := fmt.Sprintf("The number of the given arguments (%d) doesn't match the arity (%d), func name is %s", len(arguments), h.numIn, h.name)
		s.Log.Println(makeLoginfo(conn, errMsg))
		if msgType == REQUEST {
			s.sendErrorResponseMessage(conn, msgId, errMsg)
		}
		return
	}

	inParams := make([]reflect.Value, h.numIn)
	err = toReflectValues(inParams, arguments, h.fInTypes)
	if err != nil {
		s.Log.Println(makeLoginfo(conn, err))
		if msgType == REQUEST {
			s.sendErrorResponseMessage(conn, msgId, err.Error())
		}
		return
	}

	var rvs []reflect.Value
	var errMsg string
	rvs, errMsg = s.funcCall(h, inParams)
	if errMsg != "" {
		s.Log.Println(makeLoginfo(conn, errMsg))
		if msgType == REQUEST {
			s.sendErrorResponseMessage(conn, msgId, errMsg)
		}
		return
	}

	var retVal reflect.Value
	switch h.modeOut {
	case modeOutErr:
		errMsg = rvs[0].String()
	case modeOutVal:
		retVal = rvs[0]
	case modeOutValAndErr:
		retVal = rvs[0]
		errMsg = rvs[1].String()
	}

	if errMsg != "" {
		if msgType == REQUEST {
			s.sendErrorResponseMessage(conn, msgId, errMsg)
		} else {
			s.Log.Println(makeLoginfo(conn, errMsg))
		}
		return
	}

	if msgType == REQUEST {
		s.sendResponseMessage(conn, msgId, retVal.Interface())
	}
}

func (s *Server) funcCall(h *handler, in []reflect.Value) (rvs []reflect.Value, errMsg string) {
	defer func() {
		if err := recover(); err != nil {
			errMsg = fmt.Sprintf("%v, func name is %s", err, h.name)
		}
	}()
	rvs = h.f.Call(in)
	return
}

func (s *Server) sendErrorResponseMessage(writer net.Conn, msgId int, errMsg string) (err error) {
	var buf bytes.Buffer
	buf.WriteByte(0x94)
	enc := msgpack.NewEncoder(&buf)
	err = enc.EncodeMulti(uint8(RESPONSE), msgId, errMsg, nil)
	if err != nil {
		return
	}
	err = s.write(writer, buf.Bytes())
	return
}

func (s *Server) sendResponseMessage(writer net.Conn, msgId int, value interface{}) (err error) {
	var buf bytes.Buffer
	buf.WriteByte(0x94)
	enc := msgpack.NewEncoder(&buf)
	err = enc.EncodeMulti(uint8(RESPONSE), msgId, nil, value)
	if err != nil {
		return
	}
	err = s.write(writer, buf.Bytes())
	return
}

func (s *Server) write(writer net.Conn, data []byte) (err error) {
	if rconn, ok := writer.(*rpcConn); ok {
		select {
		case rconn.WriteChan <- data:
		case <-time.After(3 * time.Second):
			err = errors.New("Write WriteChan timeout!")
		}
	} else {
		_, err = writer.Write(data)
		if err != nil {
			s.Log.Println(makeLoginfo(writer, err))
		}
	}
	return
}
