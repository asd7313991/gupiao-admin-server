-- 历史持仓口径第二轮明细盘点，强制只读，不转换、不修复数据。
-- 不导出姓名、电话、证件、银行卡、密码或自由填写的备注。
-- 每笔持仓相关成交最多导出最近200条；超过时明确标记，不视作完整历史。
-- 冻结资金同时核对未完成挂单与待审核提现，不能把无挂单等同于冻结异常。
BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '60s';
SET LOCAL lock_timeout = '5s';

WITH positions AS (
    SELECT p.id, p.customer_id, p.symbol, p.stock_name, p.currency,
           p.position_qty, p.available_qty, p.cost_price, p.total_cost,
           p.margin, p.leverage, p.status, p.buy_at, p.created_at, p.updated_at
    FROM trade_positions p
    WHERE p.deleted_at IS NULL AND p.position_qty > 0
),
account_ids AS (
    SELECT customer_id FROM positions
    UNION
    SELECT customer_id FROM limit_orders WHERE deleted_at IS NULL AND status = 'pending'
    UNION
    SELECT id AS customer_id FROM customers
    WHERE deleted_at IS NULL AND (frozen_balance <> 0 OR hkd_frozen_balance <> 0)
),
account_review AS (
    SELECT a.customer_id, c.id IS NOT NULL AS customer_exists,
           c.deleted_at IS NOT NULL AS customer_deleted,
           c.status, c.fund_status, c.balance, c.hkd_balance,
           c.frozen_balance, c.hkd_frozen_balance,
           coalesce(w.pending_withdrawal, 0) AS pending_cny_withdrawal,
           coalesce(o.cny_buy_orders, 0) AS pending_cny_buy_frozen,
           coalesce(o.hkd_buy_orders, 0) AS pending_hkd_buy_frozen,
           c.frozen_balance::numeric - coalesce(w.pending_withdrawal, 0)
               - coalesce(o.cny_buy_orders, 0) AS unexplained_cny_frozen_requires_review,
           c.hkd_frozen_balance::numeric - coalesce(o.hkd_buy_orders, 0)
               AS unexplained_hkd_frozen_requires_review
    FROM account_ids a
    LEFT JOIN customers c ON c.id = a.customer_id
    LEFT JOIN LATERAL (
        SELECT sum(f.amount::numeric) AS pending_withdrawal
        FROM finance_withdrawals f
        WHERE f.customer_id = a.customer_id AND f.deleted_at IS NULL AND f.status = 2
    ) w ON true
    LEFT JOIN LATERAL (
        SELECT sum(l.frozen_amount::numeric) FILTER (
                   WHERE upper(trim(l.symbol)) NOT LIKE '%.HK') AS cny_buy_orders,
               sum(l.frozen_amount::numeric) FILTER (
                   WHERE upper(trim(l.symbol)) LIKE '%.HK') AS hkd_buy_orders
        FROM limit_orders l
        WHERE l.customer_id = a.customer_id AND l.deleted_at IS NULL
          AND l.status = 'pending' AND l.direction = '买入'
    ) o ON true
),
trade_details AS (
    SELECT p.id AS position_id, count(r.id) AS total_related_records,
           count(r.id) > 200 AS history_truncated,
           coalesce(jsonb_agg(jsonb_build_object(
               'record_id', r.id, 'direction', r.direction,
               'quantity', r.quantity, 'price', r.trade_price, 'amount', r.amount,
               'commission', r.commission, 'stamp_duty', r.stamp_duty,
               'transfer_fee', r.transfer_fee, 'trade_at', r.trade_at,
               'created_at', r.created_at, 'updated_at', r.updated_at,
               'deleted_at', r.deleted_at
           ) ORDER BY r.created_at, r.id) FILTER (WHERE r.id IS NOT NULL AND r.row_number <= 200),
           '[]'::jsonb) AS recent_records
    FROM positions p
    LEFT JOIN LATERAL (
        SELECT t.*, row_number() OVER (ORDER BY t.created_at DESC, t.id DESC) AS row_number
        FROM trade_records t
        WHERE t.customer_id = p.customer_id
          AND upper(trim(t.symbol)) = upper(trim(p.symbol))
    ) r ON true
    GROUP BY p.id
),
withdrawals AS (
    SELECT f.id, f.customer_id, f.amount, f.currency, f.status,
           f.created_at, f.updated_at, f.reviewed_at
    FROM finance_withdrawals f
    WHERE f.deleted_at IS NULL AND f.status = 2
      AND f.customer_id IN (SELECT customer_id FROM account_ids)
),
orders AS (
    SELECT l.id, l.customer_id, l.symbol, l.direction, l.quantity,
           l.limit_price, l.status, l.frozen_amount, l.frozen_quantity,
           l.created_at, l.updated_at, l.filled_at, l.cancelled_at, l.trade_record_id
    FROM limit_orders l
    WHERE l.customer_id IN (SELECT customer_id FROM account_ids)
      AND l.deleted_at IS NULL AND l.status = 'pending'
)
SELECT jsonb_pretty(jsonb_build_object(
    'report_version', 'position-basis-details-v1',
    'generated_at_beijing', to_char(current_timestamp AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD HH24:MI:SS'),
    'transaction_read_only', current_setting('transaction_read_only'),
    'warning', '只读盘点；成交记录无原始杠杆快照和人工录入标记，不能仅凭数量相等自动转换。冻结差额只作核对线索，不是自动退款依据。',
    'accounts', coalesce((SELECT jsonb_agg(to_jsonb(a) ORDER BY a.customer_id) FROM account_review a), '[]'::jsonb),
    'positions', coalesce((SELECT jsonb_agg(to_jsonb(p) ORDER BY p.customer_id, p.id) FROM positions p), '[]'::jsonb),
    'position_trades', coalesce((SELECT jsonb_agg(to_jsonb(t) ORDER BY t.position_id) FROM trade_details t), '[]'::jsonb),
    'pending_withdrawals', coalesce((SELECT jsonb_agg(to_jsonb(w) ORDER BY w.customer_id, w.id) FROM withdrawals w), '[]'::jsonb),
    'pending_orders', coalesce((SELECT jsonb_agg(to_jsonb(o) ORDER BY o.customer_id, o.id) FROM orders o), '[]'::jsonb)
)) AS position_basis_details_report;

ROLLBACK;