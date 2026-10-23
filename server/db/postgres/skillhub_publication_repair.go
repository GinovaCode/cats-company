package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationRepairStore = (*Adapter)(nil)

func lockPublicationRepairAuthority(ctx context.Context, tx *sql.Tx, actor int64, id string) (*store.SkillHubPublicationEntry, error) {
	e, err := lockPublicationCommitEntry(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if e.Input.ActorUID != actor {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err := lockPublicationCommitAuthority(ctx, tx, e); err != nil {
		return nil, err
	}
	var blocked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=$1)
		OR EXISTS(SELECT 1 FROM skillhub_publication_commit_intents WHERE entry_id=$1)`, id).Scan(&blocked); err != nil {
		return nil, err
	}
	if blocked {
		return nil, store.ErrSkillHubPublicationSettled
	}
	return e, nil
}

func readPublicationRepairTask(ctx context.Context, tx *sql.Tx, id string) (*store.SkillHubPublicationRepairTask, error) {
	t := &store.SkillHubPublicationRepairTask{}
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT task_id,input,identity_hash,created_at FROM skillhub_publication_repair_tasks WHERE entry_id=$1`, id).
		Scan(&t.ID, &raw, &t.IdentityHash, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &t.Input) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	h, err := t.Input.IdentityHash()
	if err != nil || h != t.IdentityHash || t.Input.Receipt.Query.EntryID != id {
		return nil, store.ErrSkillHubPublicationConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT round_number,request_id,input,identity_hash,lease_worker,lease_token,lease_generation,lease_until,result
		FROM skillhub_publication_repair_rounds WHERE entry_id=$1 ORDER BY round_number`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	t.Rounds = []store.SkillHubPublicationRepairRound{}
	for rows.Next() {
		r := store.SkillHubPublicationRepairRound{}
		l := &r.Lease
		l.EntryID, l.TaskID = id, t.ID
		var input, result []byte
		var hash, requestID string
		if err := rows.Scan(&l.Round, &requestID, &input, &hash, &l.WorkerID, &l.Token, &l.Generation, &l.Until, &result); err != nil {
			return nil, err
		}
		if json.Unmarshal(input, &l.Input) != nil || l.Validate() != nil || l.Round != len(t.Rounds)+1 || l.Input.RequestID != requestID {
			return nil, store.ErrSkillHubPublicationConflict
		}
		h, err := l.Input.IdentityHash()
		if err != nil || h != hash {
			return nil, store.ErrSkillHubPublicationConflict
		}
		if result != nil {
			r.Result = &store.SkillHubPublicationRepairResult{}
			if json.Unmarshal(result, r.Result) != nil || r.Result.Validate() != nil {
				return nil, store.ErrSkillHubPublicationConflict
			}
		}
		expected := t.Input.Evidence
		if len(t.Rounds) > 0 {
			if t.State() != "queued" {
				return nil, store.ErrSkillHubPublicationConflict
			}
			expected = t.Rounds[len(t.Rounds)-1].Result.Evidence
		}
		if l.Input.Evidence != expected {
			return nil, store.ErrSkillHubPublicationConflict
		}
		t.Rounds = append(t.Rounds, r)
	}
	return t, rows.Err()
}

func (a *Adapter) CreateSkillHubPublicationRepairTask(ctx context.Context, actor int64, in store.SkillHubPublicationRepairInput) (*store.SkillHubPublicationRepairTask, error) {
	hash, err := in.IdentityHash()
	if err != nil || actor <= 0 {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id := in.Receipt.Query.EntryID
	if _, err := lockPublicationRepairAuthority(ctx, tx, actor, id); err != nil {
		return nil, err
	}
	r, err := readPublicationCommitReceipt(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if r == nil || *r != in.Receipt {
		return nil, store.ErrSkillHubPublicationConflict
	}
	t, err := readPublicationRepairTask(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if t != nil {
		if t.Input != in {
			return nil, store.ErrSkillHubPublicationConflict
		}
	} else {
		suffix, err := uploadRandomHex(16)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_repair_tasks(entry_id,task_id,input,identity_hash)
			VALUES($1,$2,$3,$4)`, id, "pubrepair_"+suffix, raw, hash); err != nil {
			return nil, err
		}
		t, err = readPublicationRepairTask(ctx, tx, id)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return t, nil
}

// Original active publisher history remains available after transfer/cancel,
// but this read neither renews authority nor leaks it to the new Bot owner.
func (a *Adapter) GetSkillHubPublicationRepairTask(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationRepairTask, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var allowed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM skillhub_publication_entries e JOIN users u ON u.id=e.actor_uid
		WHERE e.entry_id=$1 AND e.actor_uid=$2 AND u.account_type='human' AND u.state=0)`, id, actor).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, nil
	}
	t, err := readPublicationRepairTask(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return t, nil
}

func (a *Adapter) ClaimSkillHubPublicationRepairRound(ctx context.Context, actor int64, id string, in store.SkillHubPublicationRepairRoundInput, worker string) (*store.SkillHubPublicationRepairRound, error) {
	hash, err := in.IdentityHash()
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || err != nil || !store.ValidSkillHubPublicationRepairWorker(worker) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := lockPublicationRepairAuthority(ctx, tx, actor, id); err != nil {
		return nil, err
	}
	t, err := readPublicationRepairTask(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, store.ErrSkillHubPublicationDenied
	}
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	number := len(t.Rounds) + 1
	reclaim := false
	for _, r := range t.Rounds {
		if r.Lease.Input.RequestID != in.RequestID {
			continue
		}
		if r.Lease.Input != in {
			return nil, store.ErrSkillHubPublicationConflict
		}
		if r.Result != nil || (r.Lease.WorkerID == worker && r.Lease.Until.After(now)) {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return &r, nil
		}
		if r.Lease.Until.After(now) {
			return nil, store.ErrSkillHubPublicationBusy
		}
		number, reclaim = r.Lease.Round, true
	}
	if !reclaim {
		switch t.State() {
		case "running":
			return nil, store.ErrSkillHubPublicationBusy
		case "stopped_budget":
			return nil, store.ErrSkillHubPublicationExhausted
		case "queued":
		default:
			return nil, store.ErrSkillHubPublicationSettled
		}
		expected := t.Input.Evidence
		if len(t.Rounds) > 0 {
			expected = t.Rounds[len(t.Rounds)-1].Result.Evidence
		}
		if expected != in.Evidence {
			return nil, store.ErrSkillHubPublicationConflict
		}
	}
	token, err := uploadRandomHex(16)
	if err != nil {
		return nil, err
	}
	if reclaim {
		_, err = tx.ExecContext(ctx, `UPDATE skillhub_publication_repair_rounds SET lease_worker=$3,lease_token=$4,
			lease_generation=lease_generation+1,lease_until=clock_timestamp()+$5*interval '1 second' WHERE entry_id=$1 AND round_number=$2`, id, number, worker, token, store.SkillHubPublicationRepairLeaseTTL.Seconds())
	} else {
		raw, marshalErr := json.Marshal(in)
		if marshalErr != nil {
			return nil, marshalErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_repair_rounds
			(entry_id,round_number,request_id,input,identity_hash,lease_worker,lease_token,lease_generation,lease_until)
			VALUES($1,$2,$3,$4,$5,$6,$7,1,clock_timestamp()+$8*interval '1 second')`, id, number, in.RequestID, raw, hash, worker, token, store.SkillHubPublicationRepairLeaseTTL.Seconds())
	}
	if err != nil {
		return nil, err
	}
	t, err = readPublicationRepairTask(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &t.Rounds[number-1], nil
}

func (a *Adapter) FinishSkillHubPublicationRepairRound(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, result store.SkillHubPublicationRepairResult) error {
	if actor <= 0 || lease.Validate() != nil || result.Validate() != nil {
		return store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := lockPublicationRepairAuthority(ctx, tx, actor, lease.EntryID); err != nil {
		return err
	}
	t, err := readPublicationRepairTask(ctx, tx, lease.EntryID)
	if err != nil {
		return err
	}
	if t == nil || t.ID != lease.TaskID || len(t.Rounds) < lease.Round {
		return store.ErrSkillHubPublicationStale
	}
	r := t.Rounds[lease.Round-1]
	if r.Lease != lease {
		return store.ErrSkillHubPublicationStale
	}
	if r.Result != nil {
		if *r.Result != result {
			return store.ErrSkillHubPublicationConflict
		}
		return tx.Commit()
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_repair_rounds SET result=$3
		WHERE entry_id=$1 AND round_number=$2 AND lease_until>clock_timestamp()`, lease.EntryID, lease.Round, raw)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return store.ErrSkillHubPublicationStale
	}
	return tx.Commit()
}
