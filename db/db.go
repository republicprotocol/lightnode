package db

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"github.com/renproject/darknode/abi"
)

type TxStatus int8

const (
	TxStatusNil        = TxStatus(0)
	TxStatusConfirming = TxStatus(1)
	TxStatusConfirmed  = TxStatus(2)
)

// DB abstracts all database interactions.
type DB struct {
	db *sql.DB
}

// New creates a new DB instance.
func New(db *sql.DB) DB {
	return DB{
		db: db,
	}
}

// Init creates the tables for storing txs if it does not exist. Multiple calls
// of this function will only create the tables once and not return an error.
func (db DB) Init() error {
	// TODO: Decide approach for versioning database tables.
	shiftIn := `CREATE TABLE IF NOT EXISTS shiftin (
    hash                 CHAR(64) NOT NULL PRIMARY KEY,
    status               BIGINT,
    created_time         INT, 
    contract             VARCHAR(255),
    phash                CHAR(64),
    token                CHAR(40),
    toAddr               CHAR(40),
    n                    CHAR(64),
    amount               BIGINT,
	ghash                CHAR(64),
	nhash                CHAR(64),
	sighash              CHAR(64),
	utxo_tx_hash         CHAR(64),
    utxo_vout            INT
);`
	_, err := db.db.Exec(shiftIn)
	if err != nil {
		return err
	}

	shiftOut := `CREATE TABLE IF NOT EXISTS shiftout (
    hash                 CHAR(64) NOT NULL PRIMARY KEY,
    status               INT,
    created_time         INT,
    contract             VARCHAR(255), 
    ref                  BIGINT, 
    toAddr               VARCHAR(255),
    amount               BIGINT
);`
	_, err = db.db.Exec(shiftOut)
	return err
}

// InsertShiftIn stores a shift in tx to the database.
func (db DB) InsertShiftIn(tx abi.Tx) error {
	phash, ok := tx.In.Get("phash").Value.(abi.B32)
	if !ok {
		return fmt.Errorf("unexpected type for phash, expected abi.B32, got %v", tx.In.Get("phash").Value.Type())
	}
	amount, ok := tx.In.Get("amount").Value.(abi.U256)
	if !ok {
		return fmt.Errorf("unexpected type for amount, expected abi.U256, got %v", tx.In.Get("amount").Value.Type())
	}
	token, ok := tx.In.Get("token").Value.(abi.ExtEthCompatAddress)
	if !ok {
		return fmt.Errorf("unexpected type for token, expected abi.ExtEthCompatAddress, got %v", tx.In.Get("token").Value.Type())
	}
	to, ok := tx.In.Get("to").Value.(abi.ExtEthCompatAddress)
	if !ok {
		return fmt.Errorf("unexpected type for to, expected abi.ExtEthCompatAddress, got %v", tx.In.Get("to").Value.Type())
	}
	n, ok := tx.In.Get("n").Value.(abi.B32)
	if !ok {
		return fmt.Errorf("unexpected type for n, expected abi.B32, got %v", tx.In.Get("n").Value.Type())
	}
	utxo, ok := tx.In.Get("utxo").Value.(abi.ExtBtcCompatUTXO)
	if !ok {
		return fmt.Errorf("unexpected type for utxo, expected abi.ExtTypeBtcCompatUTXO, got %v", tx.In.Get("utxo").Value.Type())
	}
	ghash, ok := tx.Autogen.Get("ghash").Value.(abi.B32)
	if !ok {
		return fmt.Errorf("unexpected type for ghash, expected abi.B32, got %v", tx.In.Get("ghash").Value.Type())
	}
	nhash, ok := tx.Autogen.Get("nhash").Value.(abi.B32)
	if !ok {
		return fmt.Errorf("unexpected type for nhash, expected abi.B32, got %v", tx.In.Get("nhash").Value.Type())
	}
	sighash, ok := tx.Autogen.Get("sighash").Value.(abi.B32)
	if !ok {
		return fmt.Errorf("unexpected type for sighash, expected abi.B32, got %v", tx.In.Get("sighash").Value.Type())
	}

	script := `INSERT INTO shiftin (hash, status, created_time, contract, phash, token, toAddr, n, amount, ghash, nhash, sighash, utxo_tx_hash, utxo_vout)
VALUES ($1, 1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) ON CONFLICT DO NOTHING;`
	_, err := db.db.Exec(script,
		hex.EncodeToString(tx.Hash[:]),
		time.Now().Unix(),
		tx.To,
		hex.EncodeToString(phash[:]),
		hex.EncodeToString(token[:]),
		hex.EncodeToString(to[:]),
		hex.EncodeToString(n[:]),
		amount.Int.Int64(),
		hex.EncodeToString(ghash[:]),
		hex.EncodeToString(nhash[:]),
		hex.EncodeToString(sighash[:]),
		hex.EncodeToString(utxo.TxHash[:]),
		utxo.VOut.Int.Int64(),
	)
	return err
}

// InsertShiftOut stores a shift out tx to the database.
func (db DB) InsertShiftOut(tx abi.Tx) error {
	ref, ok := tx.In.Get("ref").Value.(abi.U64)
	if !ok {
		return fmt.Errorf("unexpected type for ref, expected abi.U64, got %v", tx.In.Get("ref").Value.Type())
	}
	to, ok := tx.In.Get("to").Value.(abi.B)
	if !ok {
		return fmt.Errorf("unexpected type for to, expected abi.B, got %v", tx.In.Get("to").Value.Type())
	}
	amount, ok := tx.In.Get("amount").Value.(abi.U256)
	if !ok {
		return fmt.Errorf("unexpected type for amount, expected abi.U256, got %v", tx.In.Get("amount").Value.Type())
	}

	script := `INSERT INTO shiftout (hash, status, created_time, contract, ref, toAddr, amount) 
VALUES ($1, 1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING;`
	_, err := db.db.Exec(script,
		hex.EncodeToString(tx.Hash[:]),
		time.Now().Unix(),
		tx.To,
		ref.Int.Int64(),
		hex.EncodeToString(to),
		amount.Int.Int64(),
	)
	return err
}

// ShiftIn returns the shift in tx with the given hash.
func (db DB) ShiftIn(txHash abi.B32) (abi.Tx, error) {
	var contract, phash, token, to, n, ghash, nhash, sighash, utxoHash string
	var amount, utxoVout int
	err := db.db.QueryRow("SELECT contract, phash, token, toAddr, n, amount, ghash, nhash, sighash, utxo_tx_hash, utxo_vout FROM shiftin WHERE hash = $1", hex.EncodeToString(txHash[:])).Scan(
		&contract, &phash, &token, &to, &n, &amount, &ghash, &nhash, &sighash, &utxoHash, &utxoVout)
	if err != nil {
		return abi.Tx{}, err
	}
	return constructShiftIn(txHash, contract, phash, token, to, n, ghash, nhash, sighash, utxoHash, amount, utxoVout)
}

// ShiftOut returns the shift out tx with the given hash.
func (db DB) ShiftOut(txHash abi.B32) (abi.Tx, error) {
	var contract, to string
	var ref, amount int
	err := db.db.QueryRow("SELECT contract, ref, toAddr, amount FROM shiftout WHERE hash = $1", hex.EncodeToString(txHash[:])).Scan(
		&contract, &ref, &to, &amount)
	if err != nil {
		return abi.Tx{}, err
	}
	return constructShiftOut(txHash, contract, to, ref, amount)
}

// PendingTxs returns all pending txs from the database which have not yet
// expired.
func (db DB) PendingTxs() (abi.Txs, error) {
	txs := make(abi.Txs, 0, 128)

	// Get pending shift in txs.
	shiftIns, err := db.db.Query(`SELECT hash, contract, phash, token, toAddr, n, amount, ghash, nhash, sighash, utxo_tx_hash, utxo_vout FROM shiftin 
		WHERE status = $1 AND $2 - created_time < 86400`, TxStatusConfirming, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer shiftIns.Close()

	for shiftIns.Next() {
		var hash, contract, phash, token, to, n, ghash, nhash, sighash, utxoHash string
		var amount, utxoVout int
		err = shiftIns.Scan(&hash, &contract, &phash, &token, &to, &n, &amount, &ghash, &nhash, &sighash, &utxoHash, &utxoVout)
		if err != nil {
			return nil, err
		}

		txHash, err := stringToB32(hash)
		if err != nil {
			return nil, err
		}
		tx, err := constructShiftIn(txHash, contract, phash, token, to, n, ghash, nhash, sighash, utxoHash, amount, utxoVout)
		if err != nil {
			return nil, err
		}
		txs = append(txs, tx)
	}
	if shiftIns.Err() != nil {
		return nil, err
	}

	// Get pending shift out txs.
	shiftOuts, err := db.db.Query(`SELECT hash, contract, ref, toAddr, amount FROM shiftout 
		WHERE status = $1 AND $2 - created_time < 86400`, TxStatusConfirming, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer shiftOuts.Close()

	for shiftOuts.Next() {
		var hash, contract, to string
		var ref, amount int

		err = shiftOuts.Scan(&hash, &contract, &ref, &to, &amount)
		if err != nil {
			return nil, err
		}

		txHash, err := stringToB32(hash)
		if err != nil {
			return nil, err
		}
		tx, err := constructShiftOut(txHash, contract, to, ref, amount)
		if err != nil {
			return nil, err
		}
		txs = append(txs, tx)
	}
	return txs, shiftOuts.Err()
}

// Prune deletes txs which have expired based on the given expiry.
func (db DB) Prune(expiry time.Duration) error {
	_, err := db.db.Exec("DELETE FROM shiftin WHERE $1 - created_time > $2;", time.Now().Unix(), int(expiry.Seconds()))
	if err != nil {
		return err
	}

	_, err = db.db.Exec("DELETE FROM shiftout WHERE $1 - created_time > $2;", time.Now().Unix(), int(expiry.Seconds()))
	return err
}

// Confirmed returns whether or not the tx with the given hash has received
// sufficient confirmations.
func (db DB) Confirmed(hash abi.B32) (bool, error) {
	var status int
	err := db.db.QueryRow(`SELECT status FROM shiftin WHERE hash = $1;`,
		hex.EncodeToString(hash[:])).Scan(&status)
	if err == sql.ErrNoRows {
		err = db.db.QueryRow(`SELECT status FROM shiftout WHERE hash = $1;`,
			hex.EncodeToString(hash[:])).Scan(&status)
	}
	return TxStatus(status) == TxStatusConfirmed, err
}

// ConfirmTx sets the transaction status to confirmed.
func (db DB) ConfirmTx(hash abi.B32) error {
	_, err := db.db.Exec("UPDATE shiftin SET status = $1 WHERE hash = $2;", TxStatusConfirmed, hex.EncodeToString(hash[:]))
	if err != nil {
		return err
	}
	_, err = db.db.Exec("UPDATE shiftout SET status = $1 WHERE hash = $2;", TxStatusConfirmed, hex.EncodeToString(hash[:]))
	return err
}

// constructShiftIn constructs a transaction using the data queried from the
// database.
func constructShiftIn(hash abi.B32, contract, phash, token, to, n, ghash, nhash, sighash, utxoHash string, amount, utxoVout int) (abi.Tx, error) {
	tx := abi.Tx{
		Hash: hash,
		To:   abi.Address(contract),
	}
	phashArg, err := decodeB32("phash", phash)
	if err != nil {
		return abi.Tx{}, err
	}
	tokenArg, err := decodeEthAddress("token", token)
	if err != nil {
		return abi.Tx{}, err
	}
	toArg, err := decodeEthAddress("to", to)
	if err != nil {
		return abi.Tx{}, err
	}
	nArg, err := decodeB32("n", n)
	if err != nil {
		return abi.Tx{}, err
	}
	amountArg := abi.Arg{
		Name:  "amount",
		Type:  abi.TypeU256,
		Value: abi.U256{Int: big.NewInt(int64(amount))},
	}
	ghashArg, err := decodeB32("ghash", ghash)
	if err != nil {
		return abi.Tx{}, err
	}
	nhashArg, err := decodeB32("nhash", nhash)
	if err != nil {
		return abi.Tx{}, err
	}
	sighashArg, err := decodeB32("sighash", sighash)
	if err != nil {
		return abi.Tx{}, err
	}
	utxoHashArg, err := decodeB32("utxo", utxoHash)
	if err != nil {
		return abi.Tx{}, err
	}
	utxoArg := abi.Arg{
		Name: "utxo",
		Type: abi.ExtTypeBtcCompatUTXO,
		Value: abi.ExtBtcCompatUTXO{
			TxHash: utxoHashArg.Value.(abi.B32),
			VOut:   abi.U32{Int: big.NewInt(int64(utxoVout))},
		},
	}
	tx.In.Append(phashArg, tokenArg, toArg, nArg, utxoArg, amountArg)
	tx.Autogen.Append(ghashArg, nhashArg, sighashArg)

	return tx, nil
}

// constructShiftOut constructs a transaction using the data queried from the
// database.
func constructShiftOut(hash abi.B32, contract, to string, ref, amount int) (abi.Tx, error) {
	tx := abi.Tx{
		Hash: hash,
		To:   abi.Address(contract),
	}
	toBytes, err := hex.DecodeString(to)
	if err != nil {
		return abi.Tx{}, err
	}
	refArg := abi.Arg{
		Name:  "ref",
		Type:  abi.TypeU64,
		Value: abi.U64{Int: big.NewInt(int64(ref))},
	}
	toArg := abi.Arg{
		Name:  "to",
		Type:  abi.TypeB,
		Value: abi.B(toBytes),
	}
	amountArg := abi.Arg{
		Name:  "amount",
		Type:  abi.TypeU256,
		Value: abi.U256{Int: big.NewInt(int64(amount))},
	}
	tx.In.Append(refArg, toArg, amountArg)
	return tx, nil
}

// decodeB32 decodes the value into a RenVM B32 argument.
func decodeB32(name, value string) (abi.Arg, error) {
	val, err := stringToB32(value)
	if err != nil {
		return abi.Arg{}, err
	}
	return abi.Arg{
		Name:  name,
		Type:  abi.TypeB32,
		Value: val,
	}, nil
}

// stringToB32 decoding the hex string into a RenVM B32 object.
func stringToB32(str string) (abi.B32, error) {
	decoded, err := hex.DecodeString(str)
	if err != nil {
		return abi.B32{}, err
	}
	var val abi.B32
	copy(val[:], decoded)
	return val, nil
}

// decodeEthAddress decodes the value into a RenVM ExtTypeEthCompatAddress
// argument.
func decodeEthAddress(name, value string) (abi.Arg, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return abi.Arg{}, err
	}
	var val abi.ExtEthCompatAddress
	copy(val[:], decoded)
	return abi.Arg{
		Name:  name,
		Type:  abi.ExtTypeEthCompatAddress,
		Value: val,
	}, nil
}
