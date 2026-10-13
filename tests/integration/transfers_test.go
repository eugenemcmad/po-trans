package integration

import (
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"po-trans/internal/repository/postgres"
)

// codeCheckViolation is the PostgreSQL error code of a violated CHECK constraint.
const codeCheckViolation = "23514"

// I1: a transfer moves the money in the database and keeps the total.
func TestI1_Transfer(t *testing.T) {
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}

	res := a.post(body("i1", "acc_1", "acc_2", 100))

	require.Equal(t, http.StatusOK, res.Code, res.Body)
	require.Equal(t, "completed", res.field(t, "status"))
	require.NotEmpty(t, res.field(t, "transfer_id"))
	require.JSONEq(t, `{"account_id":"acc_1","balance":900}`, a.get("/api/v1/accounts/acc_1").Body)
	require.JSONEq(t, `{"account_id":"acc_2","balance":1100}`, a.get("/api/v1/accounts/acc_2").Body)
	require.EqualValues(t, 900, balanceOf(t, pool, "acc_1"))
	requireInvariants(t, pool)
}

// I2: every rejection is stored as rejected with its reason and moves no money.
func TestI2_RejectionsAreStored(t *testing.T) {
	tests := []struct {
		name       string
		from, to   string
		amount     int64
		wantReason string
	}{
		{"insufficient funds", "acc_1", "acc_2", 1001, "insufficient funds"},
		{"unknown sender", "acc_99", "acc_2", 1, "account not found"},
		{"unknown receiver", "acc_1", "acc_99", 1, "account not found"},
		{"self transfer", "acc_1", "acc_1", 1, "self-transfer not allowed"},
		{"self transfer of an unknown account", "acc_99", "acc_99", 1, "self-transfer not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := freshSeeded(t)
			a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}

			res := a.post(body("i2", tt.from, tt.to, tt.amount))

			require.Equal(t, http.StatusUnprocessableEntity, res.Code, res.Body)
			require.JSONEq(t, `{"status":"rejected","reason":"`+tt.wantReason+`"}`, res.Body)
			require.Equal(t, 1, count(t, pool,
				`SELECT count(*) FROM transfers WHERE idempotency_key = 'i2' AND status = 'rejected' AND reason = $1`, tt.wantReason))
			require.EqualValues(t, 1000, balanceOf(t, pool, "acc_1"), "a rejection moves no money")
			requireInvariants(t, pool)
		})
	}
}

// I3: the same key with other data is 409 (rule 2).
func TestI3_SameKeyOtherData(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"other amount", body("i3", "acc_1", "acc_2", 101)},
		{"other receiver", body("i3", "acc_1", "acc_3", 100)},
		{"other sender", body("i3", "acc_4", "acc_2", 100)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := freshSeeded(t)
			a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}
			require.Equal(t, http.StatusOK, a.post(body("i3", "acc_1", "acc_2", 100)).Code)

			res := a.post(tt.body)

			require.Equal(t, http.StatusConflict, res.Code, res.Body)
			require.JSONEq(t, `{"status":"error","reason":"idempotency key reused with different parameters"}`, res.Body)
			require.Equal(t, 1, count(t, pool, `SELECT count(*) FROM transfers`), "the conflict is not stored")
			require.EqualValues(t, 900, balanceOf(t, pool, "acc_1"), "and does not move money")
		})
	}
}

// I4: invalid data under a used key is 409, under a new key 400 — and nothing is stored for it.
func TestI4_InvalidFieldWithExistingKey(t *testing.T) {
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}
	require.Equal(t, http.StatusOK, a.post(body("i4", "acc_1", "acc_2", 100)).Code)

	t.Run("existing key: 409", func(t *testing.T) {
		for _, b := range []string{
			body("i4", "acc_1", "acc_2", -1),
			body("i4", "", "acc_2", 100),
			`{"idempotency_key":"i4","from_account_id":"acc_1","to_account_id":"acc_2","amount":"100"}`,
		} {
			res := a.post(b)
			require.Equal(t, http.StatusConflict, res.Code, b)
		}
	})

	t.Run("new key, same invalid body: 400", func(t *testing.T) {
		res := a.post(body("i4-new", "acc_1", "acc_2", -1))
		require.Equal(t, http.StatusBadRequest, res.Code, res.Body)
		require.JSONEq(t, `{"status":"error","reason":"amount must be a positive integer"}`, res.Body)
		require.Equal(t, 0, count(t, pool, `SELECT count(*) FROM transfers WHERE idempotency_key = 'i4-new'`))
	})
}

// I5: GET of a key that was never stored is 404.
func TestI5_GetUnknownTransfer(t *testing.T) {
	a := api{t, newServer(t, freshSeeded(t), postgres.DefaultRetryPolicy())}
	res := a.get("/api/v1/transfers/never-sent")
	require.Equal(t, http.StatusNotFound, res.Code)
	require.JSONEq(t, `{"status":"error","reason":"transfer not found"}`, res.Body)
}

// I6: rule 2 on the real database — a repeat gives the same answer and the money moves once.
func TestI6_RepeatIsNotAppliedTwice(t *testing.T) {
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}

	first := a.post(body("i6", "acc_1", "acc_2", 100))
	second := a.post(body("i6", "acc_1", "acc_2", 100))

	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, first, second, "same code and same body, same transfer_id")
	require.EqualValues(t, 900, balanceOf(t, pool, "acc_1"))
	require.Equal(t, 1, count(t, pool, `SELECT count(*) FROM transfers`))
}

// I7: a rejection is final — the repeat after a top-up is the same 422, and no money moves.
func TestI7_RepeatOfRejectedStaysRejected(t *testing.T) {
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}

	first := a.post(body("i7", "acc_1", "acc_2", 5000))
	require.Equal(t, http.StatusUnprocessableEntity, first.Code)

	setBalance(t, pool, "acc_1", 9000) // the sender is topped up; the same request would now fit
	second := a.post(body("i7", "acc_1", "acc_2", 5000))

	require.Equal(t, first, second)
	require.EqualValues(t, 9000, balanceOf(t, pool, "acc_1"), "no transfer happened")
}

// I8: GET of a rejected transfer is 200 with status rejected and its reason.
func TestI8_GetRejectedTransfer(t *testing.T) {
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}
	require.Equal(t, http.StatusUnprocessableEntity, a.post(body("i8", "acc_1", "acc_2", 5000)).Code)

	res := a.get("/api/v1/transfers/i8")

	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, "rejected", res.field(t, "status"))
	require.Equal(t, "insufficient funds", res.field(t, "reason"))
	require.Equal(t, "i8", res.field(t, "idempotency_key"))
	require.EqualValues(t, 5000, res.field(t, "amount"))
	require.NotEmpty(t, res.field(t, "transfer_id"))
	require.Regexp(t, `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`, res.field(t, "created_at"))
}

// I9: an unknown account is 422 in POST and 404 in GET.
func TestI9_UnknownAccount(t *testing.T) {
	a := api{t, newServer(t, freshSeeded(t), postgres.DefaultRetryPolicy())}

	post := a.post(body("i9", "acc_99", "acc_1", 1))
	require.Equal(t, http.StatusUnprocessableEntity, post.Code)
	require.JSONEq(t, `{"status":"rejected","reason":"account not found"}`, post.Body)

	get := a.get("/api/v1/accounts/acc_99")
	require.Equal(t, http.StatusNotFound, get.Code)
	require.JSONEq(t, `{"status":"error","reason":"account not found"}`, get.Body)
}

// I10: rule 1 at its boundary — the whole balance can be sent, then one coin more is refused.
func TestI10_WholeBalance(t *testing.T) {
	pool := freshSeeded(t)
	a := api{t, newServer(t, pool, postgres.DefaultRetryPolicy())}

	all := a.post(body("i10-all", "acc_1", "acc_2", 1000))
	require.Equal(t, http.StatusOK, all.Code, all.Body)
	require.EqualValues(t, 0, balanceOf(t, pool, "acc_1"))

	one := a.post(body("i10-one", "acc_1", "acc_2", 1))
	require.Equal(t, http.StatusUnprocessableEntity, one.Code)
	require.JSONEq(t, `{"status":"rejected","reason":"insufficient funds"}`, one.Body)
	requireInvariants(t, pool)
}

// I11: the database itself refuses what the code should never produce. The
// error must be a CHECK violation: any other error (a typo in the statement)
// would prove nothing about the constraints.
func TestI11_DatabaseConstraints(t *testing.T) {
	pool := freshSeeded(t)
	tests := []struct {
		name string
		sql  string
	}{
		{"negative balance", `UPDATE accounts SET balance = -1 WHERE id = 'acc_1'`},
		{"zero amount", `INSERT INTO transfers (id, idempotency_key, from_account_id, to_account_id, amount, status) VALUES (gen_random_uuid(), 'c1', 'a', 'b', 0, 'completed')`},
		{"unknown status", `INSERT INTO transfers (id, idempotency_key, from_account_id, to_account_id, amount, status) VALUES (gen_random_uuid(), 'c2', 'a', 'b', 1, 'pending')`},
		{"rejected without reason", `INSERT INTO transfers (id, idempotency_key, from_account_id, to_account_id, amount, status) VALUES (gen_random_uuid(), 'c3', 'a', 'b', 1, 'rejected')`},
		{"rejected with empty reason", `INSERT INTO transfers (id, idempotency_key, from_account_id, to_account_id, amount, status, reason) VALUES (gen_random_uuid(), 'c4', 'a', 'b', 1, 'rejected', '')`},
		{"completed with a reason", `INSERT INTO transfers (id, idempotency_key, from_account_id, to_account_id, amount, status, reason) VALUES (gen_random_uuid(), 'c5', 'a', 'b', 1, 'completed', 'x')`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pool.Exec(t.Context(), tt.sql)
			var pgErr *pgconn.PgError
			require.ErrorAs(t, err, &pgErr)
			require.Equal(t, codeCheckViolation, pgErr.Code, pgErr.Message)
		})
	}
}
