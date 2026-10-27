-- 在宝塔中连接本项目的 PostgreSQL 数据库，完整执行本文件。
-- 仅盘点，不执行迁移；事务强制只读，不修改持仓、余额、成交或挂单。
-- 返回一行 JSON。请完整导出该结果，保留采集时间；不要提供数据库密码。
-- 不导出姓名、手机号、身份证、银行卡、密码或认证材料。
-- 转换预览使用数据库保存的价格，不是重新获取的实时行情。
-- 成交数量对得上也不能证明从未人工修改；所有转换都需要另外确认。

BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '60s';
SET LOCAL lock_timeout = '5s';

WITH
active_positions AS (
    SELECT
        p.id,
        p.customer_id,
        p.symbol,
        p.stock_name,
        CASE
            WHEN upper(trim(p.symbol)) LIKE '%.HK' THEN 'HKD'
            ELSE coalesce(nullif(trim(p.currency), ''), 'CNY')
        END AS currency,
        coalesce(p.position_qty, 0)::numeric AS quantity,
        coalesce(p.available_qty, 0)::numeric AS available,
        coalesce(p.cost_price, 0)::numeric AS cost_price,
        coalesce(p.total_cost, 0)::numeric AS total_cost,
        coalesce(p.margin, 0)::numeric AS margin,
        coalesce(p.current_price, 0)::numeric AS saved_price,
        coalesce(p.market_value, 0)::numeric AS saved_market_value,
        coalesce(p.profit_loss, 0)::numeric AS saved_profit,
        coalesce(p.profit_rate, 0)::numeric AS saved_return_pct,
        coalesce(p.leverage, 0)::numeric AS recorded_leverage,
        CASE WHEN p.leverage >= 1 THEN p.leverage ELSE 1 END::numeric AS effective_leverage,
        p.margin_call_level,
        p.status,
        p.buy_at,
        p.created_at,
        p.updated_at,
        count(*) OVER (PARTITION BY p.customer_id, upper(trim(p.symbol))) AS same_symbol_position_count
    FROM trade_positions p
    WHERE p.deleted_at IS NULL AND p.position_qty > 0
),
pending_orders AS (
    SELECT
        o.id, o.customer_id, o.symbol, o.direction,
        o.limit_price, o.quantity, o.frozen_amount, o.frozen_quantity,
        o.created_at, o.quote_at
    FROM limit_orders o
    WHERE o.deleted_at IS NULL AND o.status = 'pending'
),
account_ids AS (
    SELECT customer_id FROM active_positions
    UNION
    SELECT customer_id FROM pending_orders
),
trade_totals AS (
    SELECT
        r.customer_id,
        upper(trim(r.symbol)) AS symbol_key,
        count(*) FILTER (WHERE r.deleted_at IS NULL) AS record_count,
        count(*) FILTER (WHERE r.deleted_at IS NOT NULL) AS deleted_record_count,
        count(*) FILTER (
            WHERE r.deleted_at IS NULL
              AND (r.direction IS NULL OR r.direction NOT IN ('买入', '卖出'))
        ) AS unknown_direction_count,
        coalesce(sum(r.quantity::numeric) FILTER (
            WHERE r.deleted_at IS NULL AND r.direction = '买入'
        ), 0) AS buy_quantity,
        coalesce(sum(r.quantity::numeric) FILTER (
            WHERE r.deleted_at IS NULL AND r.direction = '卖出'
        ), 0) AS sell_quantity,
        min(r.trade_price) FILTER (
            WHERE r.deleted_at IS NULL AND r.direction = '买入'
        ) AS min_buy_price,
        max(r.trade_price) FILTER (
            WHERE r.deleted_at IS NULL AND r.direction = '买入'
        ) AS max_buy_price,
        max(r.trade_at) FILTER (WHERE r.deleted_at IS NULL) AS last_trade_at,
        max(r.created_at) FILTER (WHERE r.deleted_at IS NULL) AS last_trade_created_at
    FROM trade_records r
    WHERE EXISTS (
        SELECT 1 FROM active_positions p
        WHERE p.customer_id = r.customer_id
          AND upper(trim(p.symbol)) = upper(trim(r.symbol))
    )
    GROUP BY r.customer_id, upper(trim(r.symbol))
),
order_totals AS (
    SELECT
        o.customer_id,
        upper(trim(o.symbol)) AS symbol_key,
        count(*) AS pending_order_count,
        coalesce(sum(o.frozen_quantity::numeric) FILTER (WHERE o.direction = '卖出'), 0) AS frozen_sell_quantity
    FROM pending_orders o
    GROUP BY o.customer_id, upper(trim(o.symbol))
),
fund_totals AS (
    SELECT
        f.customer_id,
        coalesce(nullif(trim(f.currency), ''), 'CNY') AS currency,
        f.type,
        f.direction,
        count(*) AS record_count,
        sum(f.amount::numeric) AS recorded_amount_sum,
        min(f.created_at) AS first_record_at,
        max(f.created_at) AS last_record_at
    FROM customer_fund_records f
    WHERE f.deleted_at IS NULL
      AND f.customer_id IN (SELECT customer_id FROM account_ids)
    GROUP BY f.customer_id, coalesce(nullif(trim(f.currency), ''), 'CNY'), f.type, f.direction
),
review AS (
    SELECT
        p.*,
        coalesce(t.record_count, 0) AS trade_record_count,
        coalesce(t.deleted_record_count, 0) AS deleted_trade_record_count,
        coalesce(t.unknown_direction_count, 0) AS unknown_direction_count,
        coalesce(t.buy_quantity, 0) AS recorded_buy_quantity,
        coalesce(t.sell_quantity, 0) AS recorded_sell_quantity,
        coalesce(t.buy_quantity, 0) - coalesce(t.sell_quantity, 0) AS recorded_net_quantity,
        t.min_buy_price, t.max_buy_price, t.last_trade_at, t.last_trade_created_at,
        coalesce(o.pending_order_count, 0) AS pending_order_count,
        coalesce(o.frozen_sell_quantity, 0) AS frozen_sell_quantity,
        CASE WHEN p.currency = 'HKD' THEN 1::numeric ELSE p.effective_leverage END AS conversion_factor,
        array_remove(ARRAY[
            '缺少明确的原始录入来源标记，必须人工确认',
            CASE WHEN p.same_symbol_position_count > 1 THEN '同一账户同一股票有多笔未清仓记录' END,
            CASE WHEN coalesce(t.record_count, 0) = 0 THEN '未找到有效成交记录，可能为人工录入或历史数据缺失' END,
            CASE WHEN abs(coalesce(t.buy_quantity, 0) - coalesce(t.sell_quantity, 0) - p.quantity) > 0.000001
                THEN '历史净成交数量与当前持仓不一致' END,
            CASE WHEN coalesce(t.deleted_record_count, 0) > 0 THEN '存在已删除成交记录，不能只按可见记录还原' END,
            CASE WHEN coalesce(t.unknown_direction_count, 0) > 0 THEN '存在未知成交方向，需要单独核对' END,
            CASE WHEN abs(p.total_cost - p.quantity * p.cost_price) > 0.02
                THEN '总成本与数量乘成本价不一致' END,
            CASE WHEN p.available < 0 OR p.available > p.quantity THEN '可卖数量超出正常范围' END,
            CASE WHEN abs(p.quantity - p.available - coalesce(o.frozen_sell_quantity, 0)) > 0.000001
                THEN '持有减可卖数量与卖出挂单冻结数量不一致' END,
            CASE WHEN coalesce(o.pending_order_count, 0) > 0 THEN '存在未完成挂单，切换前必须处理冻结与委托口径' END,
            CASE WHEN p.recorded_leverage < 1 THEN '杠杆原始记录不足1，旧程序按1倍处理，需要确认' END,
            CASE WHEN p.currency NOT IN ('CNY', 'HKD') THEN '币种不是人民币或港币' END,
            CASE WHEN p.currency = 'HKD' AND p.recorded_leverage <> 1 THEN '港股不是1倍全额持仓，需要单独检查' END,
            CASE WHEN p.margin <= 0 OR p.total_cost <= 0 OR p.cost_price <= 0 THEN '本金或成本非正数，不能自动转换' END,
            CASE WHEN p.margin <> p.total_cost THEN '本金与旧口径总成本不同，需核对补仓或人工调整' END,
            CASE WHEN p.status IS DISTINCT FROM 1 THEN '持仓有数量但不是启用状态' END,
            CASE WHEN p.saved_price <= 0 THEN '保存的现价无效，不能据此核对估值' END,
            CASE WHEN p.quantity::text IN ('NaN', 'Infinity', '-Infinity')
                   OR p.available::text IN ('NaN', 'Infinity', '-Infinity')
                   OR p.cost_price::text IN ('NaN', 'Infinity', '-Infinity')
                   OR p.total_cost::text IN ('NaN', 'Infinity', '-Infinity')
                   OR p.margin::text IN ('NaN', 'Infinity', '-Infinity')
                   OR p.saved_price::text IN ('NaN', 'Infinity', '-Infinity')
                   OR p.recorded_leverage::text IN ('NaN', 'Infinity', '-Infinity')
                THEN '存在非有限数值，预览不具备转换依据' END
        ]::text[], NULL) AS review_notes
    FROM active_positions p
    LEFT JOIN trade_totals t ON t.customer_id = p.customer_id AND t.symbol_key = upper(trim(p.symbol))
    LEFT JOIN order_totals o ON o.customer_id = p.customer_id AND o.symbol_key = upper(trim(p.symbol))
),
position_report AS (
    SELECT
        r.id AS position_id,
        r.customer_id,
        r.symbol,
        r.stock_name,
        r.currency,
        r.created_at,
        r.updated_at,
        r.buy_at,
        r.status,
        r.margin_call_level,
        true AS requires_manual_approval,
        false AS approved_for_conversion,
        r.review_notes,
        jsonb_build_object(
            'holding_quantity', r.quantity,
            'available_quantity', r.available,
            'cost_price', r.cost_price,
            'total_cost', r.total_cost,
            'margin', r.margin,
            'recorded_leverage', r.recorded_leverage,
            'effective_legacy_leverage', r.effective_leverage,
            'saved_price_not_live', r.saved_price,
            'cached_market_value_not_live', r.saved_market_value,
            'cached_profit_not_live', r.saved_profit,
            'cached_return_pct_not_live', r.saved_return_pct
        ) AS current_record,
        jsonb_build_object(
            'record_count', r.trade_record_count,
            'deleted_record_count', r.deleted_trade_record_count,
            'unknown_direction_count', r.unknown_direction_count,
            'buy_quantity', r.recorded_buy_quantity,
            'sell_quantity', r.recorded_sell_quantity,
            'net_quantity', r.recorded_net_quantity,
            'net_quantity_matches', abs(r.recorded_net_quantity - r.quantity) <= 0.000001,
            'min_buy_price', r.min_buy_price,
            'max_buy_price', r.max_buy_price,
            'last_trade_at', r.last_trade_at,
            'last_trade_created_at', r.last_trade_created_at
        ) AS trade_evidence_not_proof_of_origin,
        jsonb_build_object(
            'pending_order_count', r.pending_order_count,
            'pending_sell_frozen_quantity', r.frozen_sell_quantity
        ) AS pending_order_evidence,
        CASE WHEN r.currency = 'CNY'
              AND r.quantity::text NOT IN ('NaN', 'Infinity', '-Infinity')
              AND r.available::text NOT IN ('NaN', 'Infinity', '-Infinity')
              AND r.cost_price::text NOT IN ('NaN', 'Infinity', '-Infinity')
              AND r.total_cost::text NOT IN ('NaN', 'Infinity', '-Infinity')
              AND r.margin::text NOT IN ('NaN', 'Infinity', '-Infinity')
              AND r.saved_price::text NOT IN ('NaN', 'Infinity', '-Infinity')
              AND r.recorded_leverage::text NOT IN ('NaN', 'Infinity', '-Infinity')
              AND r.total_cost > 0 AND r.cost_price > 0 AND r.margin > 0 AND r.saved_price > 0
        THEN jsonb_build_object(
            'proposal_only', true,
            'actual_holding_quantity', r.quantity * r.conversion_factor,
            'actual_available_quantity', r.available * r.conversion_factor,
            'unchanged_cost_price', r.cost_price,
            'full_purchase_cost', r.total_cost * r.conversion_factor,
            'unchanged_margin', r.margin,
            'unchanged_balance', true,
            'has_fractional_actual_quantity', r.quantity * r.conversion_factor <> trunc(r.quantity * r.conversion_factor),
            'old_market_value_at_saved_price', round(r.quantity * r.saved_price * r.effective_leverage, 2),
            'new_market_value_at_same_saved_price', round(r.quantity * r.conversion_factor * r.saved_price, 2),
            'old_profit_at_saved_price', round((r.quantity * r.saved_price - r.total_cost) * r.effective_leverage, 2),
            'new_profit_at_same_saved_price', round(
                r.quantity * r.conversion_factor * r.saved_price - r.total_cost * r.conversion_factor, 2
            ),
            'old_principal_return_pct', round(
                round((r.quantity * r.saved_price - r.total_cost) * r.effective_leverage, 2) / r.total_cost * 100, 6
            ),
            'new_stock_return_pct', round(
                round(r.quantity * r.conversion_factor * r.saved_price - r.total_cost * r.conversion_factor, 2)
                    / (r.total_cost * r.conversion_factor) * 100, 6
            )
        ) ELSE NULL END AS hypothetical_legacy_conversion_not_approved
    FROM review r
)
SELECT jsonb_pretty(jsonb_build_object(
    'report_version', 'position-basis-audit-v1',
    'generated_at_beijing', to_char(current_timestamp AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD HH24:MI:SS'),
    'transaction_read_only', current_setting('transaction_read_only'),
    'warning', '这是只读盘点，不是转换指令。保存价格不代表实时价，净成交匹配也不能证明没有人工改动。历史成交不按乘杠杆重写，挂单及费用必须另行核对。',
    'active_position_count', (SELECT count(*) FROM active_positions),
    'pending_order_count', (SELECT count(*) FROM pending_orders),
    'accounts', coalesce((
        SELECT jsonb_agg(jsonb_build_object(
            'customer_id', c.id,
            'status', c.status,
            'fund_status', c.fund_status,
            'customer_deleted', c.deleted_at IS NOT NULL,
            'cny_balance', c.balance,
            'hkd_balance', c.hkd_balance,
            'cny_frozen_balance', c.frozen_balance,
            'hkd_frozen_balance', c.hkd_frozen_balance,
            'strategy_balance', c.strategy_balance,
            'recorded_total_profit', c.total_profit,
            'recorded_total_loss', c.total_loss
        ) ORDER BY c.id)
        FROM customers c
        WHERE c.id IN (SELECT customer_id FROM account_ids)
    ), '[]'::jsonb),
    'positions', coalesce((
        SELECT jsonb_agg(to_jsonb(p) ORDER BY p.customer_id, p.symbol, p.position_id)
        FROM position_report p
    ), '[]'::jsonb),
    'pending_orders', coalesce((
        SELECT jsonb_agg(to_jsonb(o) ORDER BY o.customer_id, o.id)
        FROM pending_orders o
    ), '[]'::jsonb),
    'fund_record_summaries_not_balance_reconciliation', coalesce((
        SELECT jsonb_agg(to_jsonb(f) ORDER BY f.customer_id, f.currency, f.type, f.direction)
        FROM fund_totals f
    ), '[]'::jsonb)
)) AS position_basis_audit_report;

ROLLBACK;
