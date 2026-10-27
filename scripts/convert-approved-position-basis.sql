-- 仅供新版服务部署并完成字段迁移后，在维护窗口手动执行。
-- 默认演练并回滚；psql -v apply=true 才提交。必须先备份数据库。
-- 仅允许用户批准的6笔，任一数量/成本/状态变化、缺账户或存在未完成挂单即整批拒绝。
-- 保留成本价、原本金、余额、冻结、已实现收益及历史成交，不给其他旧持仓转换。
\if :{?apply}
\else
\set apply false
\endif
\set ON_ERROR_STOP on
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';
LOCK TABLE customers, trade_positions, trade_records, limit_orders IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    spec record;
    p trade_positions%ROWTYPE;
    before_json text;
    affected integer;
BEGIN
    FOR spec IN SELECT * FROM (VALUES
        (15, 7::bigint, '300274.SZ', 1000::numeric, 82.49::numeric, 82490::numeric, 1),
        (17, 5::bigint, '600814.SH', 40000::numeric, 7.07::numeric, 282800::numeric, 1),
        (18, 5::bigint, '600802.SH', 40000::numeric, 6.78::numeric, 271200::numeric, 1),
        (25, 5::bigint, '002384.SZ', 800::numeric, 114.22::numeric, 91376::numeric, 2),
        (28, 1::bigint, '002242.SZ', 300::numeric, 21.09::numeric, 6327::numeric, 1),
        (29, 1::bigint, '002747.SZ', 5000::numeric, 30.44::numeric, 152200::numeric, 1)
    ) AS approved(id, customer_id, symbol, qty, cost, margin, status)
    LOOP
        SELECT * INTO STRICT p FROM trade_positions WHERE id = spec.id AND deleted_at IS NULL FOR UPDATE;
        IF p.quantity_basis <> 0 OR p.customer_id <> spec.customer_id OR p.symbol <> spec.symbol
           OR p.position_qty IS DISTINCT FROM spec.qty::double precision
           OR p.available_qty IS DISTINCT FROM spec.qty::double precision
           OR abs(p.cost_price::numeric - spec.cost) > 0.000001
           OR abs(p.total_cost::numeric - spec.margin) > 0.000001
           OR abs(p.margin::numeric - spec.margin) > 0.000001
           OR p.leverage IS DISTINCT FROM 5::double precision OR p.status IS DISTINCT FROM spec.status
           OR p.margin_call_level IS DISTINCT FROM 0 OR p.current_price IS NULL
           OR p.current_price::text IN ('NaN', 'Infinity', '-Infinity') OR p.current_price <= 0
           OR p.cost_price IS NULL OR p.total_cost IS NULL OR p.margin IS NULL
           OR p.cost_price::text IN ('NaN', 'Infinity', '-Infinity')
           OR p.total_cost::text IN ('NaN', 'Infinity', '-Infinity')
           OR p.margin::text IN ('NaN', 'Infinity', '-Infinity')
        THEN
            RAISE EXCEPTION '持仓%已变化、已转换或存在异常，整批停止，请重新盘点', spec.id;
        END IF;
        IF NOT EXISTS (SELECT 1 FROM customers WHERE id = spec.customer_id AND deleted_at IS NULL)
           OR (SELECT count(*) FROM trade_positions WHERE customer_id = spec.customer_id
               AND symbol = spec.symbol AND deleted_at IS NULL AND position_qty > 0) <> 1
           OR EXISTS (SELECT 1 FROM limit_orders WHERE customer_id = spec.customer_id
               AND symbol = spec.symbol AND status = 'pending' AND deleted_at IS NULL)
           OR EXISTS (SELECT 1 FROM trade_records WHERE customer_id = spec.customer_id
               AND symbol = spec.symbol AND (deleted_at IS NOT NULL OR direction NOT IN ('买入', '卖出')))
           OR abs(coalesce((SELECT sum(CASE direction WHEN '买入' THEN quantity::numeric
                    WHEN '卖出' THEN -quantity::numeric ELSE 0 END)
                  FROM trade_records WHERE customer_id = spec.customer_id
                    AND symbol = spec.symbol AND deleted_at IS NULL), 0) - spec.qty) > 0.000001
        THEN
            RAISE EXCEPTION '持仓%账户、重复记录、挂单或净成交核对失败，整批停止', spec.id;
        END IF;
        before_json := to_jsonb(p)::text;
        UPDATE trade_positions SET
            quantity_basis = 1,
            position_qty = position_qty * 5,
            available_qty = available_qty * 5,
            total_cost = total_cost * 5,
            market_value = round((current_price::numeric * position_qty::numeric * 5), 2),
            profit_loss = round(((current_price::numeric * position_qty::numeric - total_cost::numeric) * 5), 2),
            profit_rate = round(((current_price::numeric * position_qty::numeric - total_cost::numeric) * 5), 2)
                          / (total_cost::numeric * 5) * 100,
            status = 1,
            updated_at = current_timestamp
        WHERE id = spec.id AND quantity_basis = 0;
        GET DIAGNOSTICS affected = ROW_COUNT;
        IF affected <> 1 THEN RAISE EXCEPTION '持仓%转换行数异常', spec.id; END IF;
        INSERT INTO position_basis_conversions(position_id, batch, before_record, after_record, converted_at)
        SELECT id, 'approved-20261026-six', before_json, to_jsonb(trade_positions)::text,
               extract(epoch FROM current_timestamp)::bigint
        FROM trade_positions WHERE id = spec.id;
    END LOOP;
END $$;

SELECT id, customer_id, symbol, quantity_basis, position_qty, available_qty,
       cost_price, total_cost, margin, leverage, status
FROM trade_positions WHERE id IN (15,17,18,25,28,29) ORDER BY id;

\if :apply
COMMIT;
\echo '已提交批准的6笔转换。历史成交、账户余额和冻结资金未修改。'
\else
ROLLBACK;
\echo '仅演练，所有转换已回滚，线上数据未改变。'
\endif
