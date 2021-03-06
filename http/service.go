package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"

	"github.com/tonto/kit/http/respond"
)

// Validator interface can be implemented by endpoint request
// types and it will be automatically called by service upon decoding
type Validator interface {
	Validate() error
}

// BaseService represents base http service
type BaseService struct {
	m         sync.Mutex
	endpoints Endpoints
	mw        []Adapter
}

// Prefix returns service routing prefix
func (b *BaseService) Prefix() string { return "/" }

// RegisterHandler is a helper method that registers service HandlerFunc
// Service HandlerFunc is an extension of http.HandlerFunc which only adds context.Context
// as first parameter, the rest stays the same
func (b *BaseService) RegisterHandler(verb string, path string, h HandlerFunc, a ...Adapter) {
	if b.endpoints == nil {
		b.endpoints = make(map[string]*Endpoint)
	}
	b.endpoints[path] = &Endpoint{
		Methods: []string{verb},
		Handler: AdaptHandlerFunc(h, a...),
	}
}

// MustRegisterEndpoint panic version of RegisterEndpoint
func (b *BaseService) MustRegisterEndpoint(verb string, path string, method interface{}, a ...Adapter) {
	if err := b.RegisterEndpoint(verb, path, method, a...); err != nil {
		panic(err)
	}
}

// RegisterEndpoint is a helper method that registers service json endpoint
// JSON endpoint method should have the following signature:
// func(c context.Context, w http.ResponseWriter, req *CustomType) (*http.Response, error)
// where *CustomType is your custom request type to which r.Body will be json unmarshalled automatically
// *http.Response can be omitted if endpoint has no reasonable response, error is always required however
func (b *BaseService) RegisterEndpoint(verb string, path string, method interface{}, a ...Adapter) error {
	h, err := b.handlerFromMethod(method)
	if err != nil {
		return err
	}

	if b.endpoints == nil {
		b.endpoints = make(map[string]*Endpoint)
	}

	b.endpoints[path] = &Endpoint{
		Methods: []string{verb},
		Handler: AdaptHandlerFunc(h, a...),
	}

	return nil
}

func (b *BaseService) handlerFromMethod(m interface{}) (HandlerFunc, error) {
	err := b.validateSignature(m)
	if err != nil {
		return nil, err
	}

	return func(c context.Context, w http.ResponseWriter, r *http.Request) {
		req, err := b.decodeReq(r, m)
		if err != nil {
			respond.WithJSON(
				w, r,
				NewError(http.StatusBadRequest, fmt.Errorf("internal error: could not decode request: %v", err)),
			)
			return
		}

		if validator, ok := interface{}(req).(Validator); ok {
			err = validator.Validate()
			if err != nil {
				respond.WithJSON(
					w, r,
					NewError(http.StatusBadRequest, fmt.Errorf("could not validate request: %v", err)),
				)
				return
			}
		}

		c = context.WithValue(c, contextReqKey, r)
		v := reflect.ValueOf(m)

		b.writeResponse(
			w, r,
			v.Call([]reflect.Value{
				reflect.ValueOf(c),
				reflect.ValueOf(w),
				reflect.ValueOf(req),
			}),
		)
	}, nil
}

func (b *BaseService) validateSignature(m interface{}) error {
	t := reflect.ValueOf(m).Type()

	if t.NumIn() != 3 {
		return fmt.Errorf("incorrect endpoint signature (must have 3 params - refer to docs)")
	}

	if t.NumOut() > 2 || t.NumOut() < 1 {
		return fmt.Errorf("endpoint must return one or two values")
	}

	if t.NumOut() == 1 {
		if !t.Out(0).Implements(reflect.TypeOf((*error)(nil)).Elem()) {
			return fmt.Errorf("single endpoint return value must implement error interface")
		}
	}

	if t.NumOut() == 2 {
		if t.Out(0) != reflect.TypeOf(&Response{}) {
			return fmt.Errorf("first ret value must be of type *kit/http/Response")
		}

		if !t.Out(1).Implements(reflect.TypeOf((*error)(nil)).Elem()) {
			return fmt.Errorf("second ret value must implement error interface")
		}
	}

	if !t.In(0).Implements(reflect.TypeOf((*context.Context)(nil)).Elem()) {
		return fmt.Errorf("param one must implement context.Context")
	}

	if !t.In(1).Implements(reflect.TypeOf((*http.ResponseWriter)(nil)).Elem()) {
		return fmt.Errorf("param two must implement http.ResponseWriter")
	}

	return nil
}

func (b *BaseService) decodeReq(r *http.Request, m interface{}) (interface{}, error) {
	defer r.Body.Close()

	v := reflect.ValueOf(m)
	reqParamType := v.Type().In(2).Elem()
	req := reflect.New(reqParamType).Interface()

	err := json.NewDecoder(r.Body).Decode(req)
	if err != nil {
		return nil, fmt.Errorf("error decoding json: %v", err)
	}

	return req, nil
}

func (b *BaseService) writeResponse(w http.ResponseWriter, r *http.Request, ret []reflect.Value) {
	if len(ret) == 1 {
		if !ret[0].IsNil() {
			b.writeError(w, r, ret[0].Interface())
			return
		}
		respond.WithJSON(w, r, NewResponse(nil, http.StatusOK))
		return
	}

	if !ret[1].IsNil() {
		b.writeError(w, r, ret[1].Interface())
		return
	}

	if ret[0].IsNil() {
		respond.WithJSON(w, r, NewResponse(nil, http.StatusOK))
		return
	}

	resp := ret[0].Interface().(*Response)
	respond.WithJSON(w, r, resp)
}

func (b *BaseService) writeError(w http.ResponseWriter, r *http.Request, e interface{}) {
	if _, ok := e.(*Error); ok {
		respond.WithJSON(w, r, e)
		return
	}
	respond.WithJSON(w, r, NewError(http.StatusInternalServerError, e.(error)))
}

// Endpoints returns all registered endpoints
func (b *BaseService) Endpoints() Endpoints {
	for _, e := range b.endpoints {
		if b.mw != nil {
			e.Handler = AdaptHandlerFunc(e.Handler, b.mw...)
		}
	}
	return b.endpoints
}

// Adapt is used to adapt the service with provided adapters
func (b *BaseService) Adapt(mw ...Adapter) { b.mw = mw }
