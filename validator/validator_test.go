package validator_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/renproject/darknode"

	"github.com/renproject/darknode/jsonrpc"
	"github.com/renproject/kv"
	"github.com/renproject/lightnode/blockchain"
	"github.com/renproject/lightnode/db"
	"github.com/renproject/lightnode/http"
	"github.com/renproject/lightnode/store"
	"github.com/renproject/lightnode/testutils"
	"github.com/renproject/lightnode/validator"
	"github.com/renproject/phi"
	"github.com/sirupsen/logrus"
)

func initValidator(ctx context.Context) (phi.Sender, <-chan phi.Message) {
	opts := phi.Options{Cap: 10}
	logger := logrus.New()
	inspector, messages := testutils.NewInspector(10)
	multiStore := store.New(kv.NewTable(kv.NewMemDB(kv.JSONCodec), "addresses"))
	sqlDB, err := sql.Open("sqlite3", "./validator.db")
	Expect(err).NotTo(HaveOccurred())
	connPool := blockchain.New(logger, darknode.Localnet, common.Address{}, common.Address{}, common.Address{})
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())

	validator := validator.New(logger, inspector, multiStore, opts, key.PublicKey, connPool, db.New(sqlDB))

	go validator.Run(ctx)
	go inspector.Run(ctx)

	return validator, messages
}

var _ = Describe("Validator", func() {
	Context("When running a validator task", func() {
		It("Should pass a valid message through", func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			validator, messages := initValidator(ctx)

			for method, _ := range jsonrpc.RPCs {
				// TODO: This method is not supported right now, but when it is
				// this case should be tested too.
				if method == jsonrpc.MethodQueryEpoch {
					continue
				}

				request := testutils.ValidRequest(method)
				validator.Send(http.NewRequestWithResponder(ctx, request, ""))

				select {
				case <-time.After(time.Second):
					Fail("timeout")
				case message := <-messages:
					req, ok := message.(http.RequestWithResponder)
					Expect(ok).To(BeTrue())
					Expect(req.Request).To(Equal(request))
					Expect(req.Responder).To(Not(BeNil()))
					Eventually(req.Responder).ShouldNot(Receive())
				}
			}
		})

		It("Should return an error response when the jsonrpc field of the request is not 2.0", func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			validator, messages := initValidator(ctx)

			// TODO: Is it worth fuzz testing on the other request fields?
			request := testutils.ValidRequest(jsonrpc.MethodQueryBlock)
			request.Version = "1.0"
			req := http.NewRequestWithResponder(ctx, request, "")
			validator.Send(req)

			select {
			case <-time.After(time.Second):
				Fail("timeout")
			case res := <-req.Responder:
				expectedErr := jsonrpc.NewError(jsonrpc.ErrorCodeInvalidRequest, fmt.Sprintf("invalid jsonrpc field: expected \"2.0\", got \"%s\"", request.Version), nil)

				Expect(res.Version).To(Equal("2.0"))
				Expect(res.ID).To(Equal(request.ID))
				Expect(res.Result).To(BeNil())
				Expect(*res.Error).To(Equal(expectedErr))
				Eventually(messages).ShouldNot(Receive())
			}
		})

		It("Should return an error response when the method is not supported", func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			validator, messages := initValidator(ctx)

			// TODO: Is it worth fuzz testing on the other request fields?
			request := testutils.ValidRequest(jsonrpc.MethodQueryBlock)
			request.Method = "method"
			req := http.NewRequestWithResponder(ctx, request, "")
			validator.Send(req)

			select {
			case <-time.After(time.Second):
				Fail("timeout")
			case res := <-req.Responder:
				expectedErr := jsonrpc.NewError(jsonrpc.ErrorCodeMethodNotFound, fmt.Sprintf("unsupported method %s", request.Method), nil)

				Expect(res.Version).To(Equal("2.0"))
				Expect(res.ID).To(Equal(request.ID))
				Expect(res.Result).To(BeNil())
				Expect(*res.Error).To(Equal(expectedErr))
				Eventually(messages).ShouldNot(Receive())
			}
		})

		It("Should return an error response when the method does not match the parameters", func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			validator, messages := initValidator(ctx)

			for method := range jsonrpc.RPCs {
				// TODO: Is it worth fuzz testing on the other request fields?
				if method != jsonrpc.MethodSubmitTx && method != jsonrpc.MethodQueryTx {
					continue
				}
				params := json.RawMessage{}
				request := testutils.ValidRequest(method)
				request.Params = params
				req := http.NewRequestWithResponder(ctx, request, "")
				validator.Send(req)

				select {
				case <-time.After(time.Second):
					Fail("timeout")
				case res := <-req.Responder:
					expectedErr := jsonrpc.NewError(jsonrpc.ErrorCodeInvalidParams, "invalid parameters in request: parameters object does not match method", nil)

					Expect(res.Version).To(Equal("2.0"))
					Expect(res.ID).To(Equal(request.ID))
					Expect(res.Result).To(BeNil())
					Expect(*res.Error).To(Equal(expectedErr))
					Eventually(messages).ShouldNot(Receive())
				}
			}
		})
	})
})
