<?php
/** CLI 金额迁移；默认只预览，--apply 才执行，切换前必须暂停所有充值与计费入口。 */
if (PHP_SAPI !== 'cli') { http_response_code(404); exit; }
mysqli_report(MYSQLI_REPORT_ERROR | MYSQLI_REPORT_STRICT);

function creditMigrationInteger($value)
{
    if (!is_string($value) && !is_int($value)) throw new RuntimeException('Legacy amount must be an integer or integer text');
    $text = (string) $value;
    if (!preg_match('/^-?(0|[1-9][0-9]*)$/D', $text) || strlen(ltrim($text, '-')) > 18) {
        throw new RuntimeException('Legacy amount is not a safe integer');
    }
    $number = (int) $text;
    $limit = intdiv(PHP_INT_MAX, 1000);
    if ($number < -$limit || $number > $limit) throw new RuntimeException('Legacy amount would overflow microcredit');
    return $number;
}

function creditMigrationDisplay($tokens)
{
    $tokens = creditMigrationInteger($tokens);
    if ($tokens < 0) throw new RuntimeException('Negative recharge quantity');
    $fraction = rtrim(str_pad((string) ($tokens % 1000), 3, '0', STR_PAD_LEFT), '0');
    return (string) intdiv($tokens, 1000) . ($fraction === '' ? '' : '.' . $fraction);
}

function creditMigrationPack($text)
{
    $pack = json_decode($text);
    if (json_last_error() !== JSON_ERROR_NONE && strpos($text, '"12"') !== false) throw new RuntimeException('Malformed recharge pack JSON');
    $items = is_object($pack) ? ($pack->{'12'} ?? null) : (is_array($pack) ? ($pack[12] ?? null) : null);
    if ($items === null) return null;
    if (!is_array($items) || !$items) throw new RuntimeException('Invalid type 12 recharge pack');
    foreach ($items as $item) {
        if (!is_object($item) || property_exists($item,'credits') || !isset($item->tokens)) throw new RuntimeException('Mixed or invalid recharge units');
        $tokens = creditMigrationInteger($item->tokens);
        if ($tokens < 1) throw new RuntimeException('Invalid legacy recharge quantity');
        $item->credits = creditMigrationDisplay($tokens);
        unset($item->tokens);
    }
    if (is_object($pack)) $pack->{'12'} = $items;
    else $pack[12] = $items;
    return json_encode($pack, JSON_UNESCAPED_UNICODE | JSON_THROW_ON_ERROR);
}

/** 所有历史审计里的金额快照同步换算，保留对象/数组结构与非金额字段。 */
function creditMigrationAuditMoney($value, &$changed)
{
    if (!is_array($value) && !is_object($value)) return $value;
    foreach ($value as $key => $item) {
        if (is_string($key) && preg_match('/_micro(?:_per_1k)?$/D', $key)) {
            $item = creditMigrationInteger($item) * 1000;
            $changed = true;
        } else {
            $item = creditMigrationAuditMoney($item, $changed);
        }
        if (is_object($value)) $value->$key = $item;
        else $value[$key] = $item;
    }
    return $value;
}

function creditMigrationUpdate($db, $sql, $value, $id)
{
    $statement = $db->prepare($sql);
    $statement->bind_param('ss', $value, $id);
    $statement->execute();
    $statement->close();
}

try {
    if (PHP_INT_SIZE !== 8) throw new RuntimeException('64-bit PHP is required');
    $apply = in_array('--apply', $argv, true);
    $dsn = getenv('DATABASE_DSN');
    if (!preg_match('/^(.*)@tcp\(([^)]+)\)\/cx_ec(?:\?.*)?$/D', (string) $dsn, $match)) throw new RuntimeException('DATABASE_DSN must target cx_ec over TCP');
    $credentials = explode(':', $match[1], 2);
    if (count($credentials) !== 2 || !preg_match('/^(.*):([0-9]+)$/D', $match[2], $address)) throw new RuntimeException('Invalid database connection format');
    $db = new mysqli(trim($address[1], '[]'), $credentials[0], $credentials[1], 'cx_ec', (int) $address[2]);
    $db->set_charset('utf8mb4');
    if ($db->query('SELECT DATABASE() name')->fetch_assoc()['name'] !== 'cx_ec') throw new RuntimeException('Wrong database');
    $amounts = [
        'ai_account' => ['quota_limit_micro'],
        'ai_wallet' => ['balance_micro'],
        'ai_wallet_ledger' => ['delta_micro','balance_after_micro'],
        'ai_usage' => ['charged_micro'],
        'ai_model' => ['input_price_micro_per_1k','output_price_micro_per_1k'],
        'ai_model_pricing' => ['peak_input_price_micro_per_1k','peak_output_price_micro_per_1k','limited_input_price_micro_per_1k','limited_output_price_micro_per_1k'],
        'ai_usage_pricing' => ['input_price_micro_per_1k','output_price_micro_per_1k']
    ];
    $packTables = [
        ['cx_member','cx_auth_pack','auth_pack_id'], ['sb_cx_member','cx_auth_pack','auth_pack_id'],
        ['cx_home_new','cx_store_product_variants','pvid'], ['sb_cx_home_new','cx_store_product_variants','pvid']
    ];
    $homeSchemas = ['cx_home_new','sb_cx_home_new'];
    $required = [];
    foreach ($amounts as $table => $columns) $required[] = ['cx_ec',$table];
    $required[] = ['cx_ec','ai_admin_audit'];
    foreach ($packTables as $table) $required[] = [$table[0],$table[1]];
    foreach ($homeSchemas as $schema) $required[] = [$schema,'cx_store_shipping_content'];
    foreach ($required as [$schema,$table]) {
        $meta = $db->query("SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA='$schema' AND TABLE_NAME='$table'")->fetch_assoc();
        if (!$meta || $meta['ENGINE'] !== 'InnoDB') throw new RuntimeException('Required transactional table unavailable: '.$schema.'.'.$table);
    }
    $hasMarker = (int) $db->query("SELECT COUNT(*) n FROM information_schema.TABLES WHERE TABLE_SCHEMA='cx_ec' AND TABLE_NAME='ai_billing_unit'")->fetch_assoc()['n'] > 0;
    if ($hasMarker) {
        $engine = $db->query("SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA='cx_ec' AND TABLE_NAME='ai_billing_unit'")->fetch_assoc();
        if ($engine['ENGINE'] !== 'InnoDB') throw new RuntimeException('Billing unit marker must be transactional');
        $primary = $db->query("SELECT COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='cx_ec' AND TABLE_NAME='ai_billing_unit' AND INDEX_NAME='PRIMARY' ORDER BY SEQ_IN_INDEX")->fetch_all(MYSQLI_ASSOC);
        if (array_column($primary,'COLUMN_NAME') !== ['id']) throw new RuntimeException('Invalid billing unit marker primary key');
        $columns = $db->query("SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA='cx_ec' AND TABLE_NAME='ai_billing_unit' ORDER BY ORDINAL_POSITION")->fetch_all(MYSQLI_ASSOC);
        if (array_column($columns,'COLUMN_NAME') !== ['id','unit','micro_per_credit','migrated_at']) throw new RuntimeException('Billing unit table collides with an unrelated schema');
        foreach ($columns as $column) {
            $type = preg_replace('/\([0-9]+\)/', '', strtolower($column['COLUMN_TYPE']));
            $expected = ['id'=>'tinyint unsigned','unit'=>'varchar','micro_per_credit'=>'bigint unsigned','migrated_at'=>'timestamp'];
            if ($type !== $expected[$column['COLUMN_NAME']] || $column['IS_NULLABLE'] !== 'NO') throw new RuntimeException('Invalid billing unit marker column type');
        }
        if ($db->query('SELECT id FROM ai_billing_unit LIMIT 1')->num_rows) throw new RuntimeException('Credit migration is already applied; repeat execution is forbidden');
    }
    if ($apply) {
        // DDL 不属于后续 DML 事务；失败后标志表可为空，但不能写入完成标志。
        $sql = file_get_contents(__DIR__.'/004_credit_units.sql');
        $create = substr($sql, strpos($sql, 'CREATE TABLE'));
        $db->query($create);
        if ((int) $db->query("SELECT GET_LOCK('cx_ec.credit_migration',0) ok")->fetch_assoc()['ok'] !== 1) throw new RuntimeException('Another credit migration is running');
        $db->begin_transaction();
        // 全表当前读锁住金额与网站套餐，防止迁移执行期间交叉写入旧单位。
        foreach ($required as [$schema,$table]) $db->query("SELECT * FROM `$schema`.`$table` FOR UPDATE")->free();
        if ($db->query('SELECT id FROM ai_billing_unit FOR UPDATE')->num_rows) throw new RuntimeException('Credit migration was concurrently applied');
    }
    $pending = $db->query("SELECT COUNT(*) n FROM ai_wallet_ledger r WHERE r.reason='reserve' AND NOT EXISTS(SELECT 1 FROM ai_wallet_ledger f WHERE f.request_id=r.request_id AND f.reason IN ('release','settlement'))")->fetch_assoc()['n'];
    if ((int) $pending !== 0) throw new RuntimeException('Pending chat reservations exist; drain or resolve them before migration');
    foreach ($amounts as $table => $columns) {
        foreach ($columns as $column) {
            $range = $db->query("SELECT MIN(`$column`) minimum,MAX(`$column`) maximum FROM `$table`")->fetch_assoc();
            foreach ($range as $value) if ($value !== null) creditMigrationInteger($value);
            if ($column !== 'delta_micro' && $range['minimum'] !== null && (int) $range['minimum'] < 0) throw new RuntimeException('Negative financial state in '.$table.'.'.$column);
        }
    }
    $packUpdates = [];
    foreach ($packTables as [$schema,$table,$idColumn]) {
        $rows = $db->query("SELECT `$idColumn` id,pack FROM `$schema`.`$table` WHERE pack IS NOT NULL AND pack<>''")->fetch_all(MYSQLI_ASSOC);
        foreach ($rows as $row) {
            $converted = creditMigrationPack($row['pack']);
            if ($converted !== null) $packUpdates[] = ["UPDATE `$schema`.`$table` SET pack=? WHERE `$idColumn`=?",$converted,$row['id']];
        }
    }
    $shippingUpdates = [];
    foreach ($homeSchemas as $schema) {
        $rows = $db->query("SELECT shipping_content_id id,content FROM `$schema`.cx_store_shipping_content WHERE packs_shipping_type='12'")->fetch_all(MYSQLI_ASSOC);
        foreach ($rows as $row) $shippingUpdates[] = ["UPDATE `$schema`.cx_store_shipping_content SET content=? WHERE shipping_content_id=?",creditMigrationDisplay($row['content']),$row['id']];
    }
    $audits = $db->query("SELECT audit_id,action,details FROM ai_admin_audit WHERE details IS NOT NULL")->fetch_all(MYSQLI_ASSOC);
    $auditUpdates = [];
    foreach ($audits as $row) {
        $changed = false;
        $detail = creditMigrationAuditMoney(json_decode($row['details'],false,512,JSON_THROW_ON_ERROR), $changed);
        if ($row['action'] === 'wallet.recharge') {
            if (!is_object($detail) || !isset($detail->recharge_tokens,$detail->balance_after) || isset($detail->recharge_credit)) throw new RuntimeException('Unexpected recharge audit units');
            $detail->recharge_credit = creditMigrationDisplay($detail->recharge_tokens);
            $detail->balance_after_credit = creditMigrationDisplay($detail->balance_after);
            unset($detail->recharge_tokens,$detail->balance_after);
            $changed = true;
        }
        if ($changed) $auditUpdates[] = ['UPDATE ai_admin_audit SET details=? WHERE audit_id=?',json_encode($detail,JSON_UNESCAPED_UNICODE|JSON_THROW_ON_ERROR),$row['audit_id']];
    }
    if ($apply) {
        foreach ($amounts as $table => $columns) {
            $updates = array_map(function($column){return "`$column`=`$column`*1000";},$columns);
            if (in_array($table,['ai_account','ai_wallet','ai_model'],true)) $updates[] = 'updated_at=updated_at';
            $db->query("UPDATE `$table` SET ".implode(',',$updates));
        }
        foreach (array_merge($packUpdates,$shippingUpdates,$auditUpdates) as [$sql,$value,$id]) creditMigrationUpdate($db,$sql,$value,$id);
        $db->query("INSERT INTO ai_billing_unit(id,unit,micro_per_credit) VALUES(1,'credit',1000000)");
        $db->commit();
        $db->query("SELECT RELEASE_LOCK('cx_ec.credit_migration')");
    }
    echo json_encode(['applied'=>$apply,'financial_tables'=>count($amounts),'recharge_packs'=>count($packUpdates),'shipping_records'=>count($shippingUpdates),'financial_audits'=>count($auditUpdates),'legacy_tokens_per_credit'=>1000,'micro_per_credit'=>1000000],JSON_UNESCAPED_UNICODE).PHP_EOL;
} catch (Throwable $error) {
    if (isset($db)) { try { $db->rollback(); } catch (Throwable $rollbackError) { fwrite(STDERR,'[CreditMigration] Rollback failed; code='.$rollbackError->getCode().PHP_EOL); } }
    // 原生数据库错误不包含在面向操作者的输出中，避免连接信息泄漏。
    fwrite(STDERR,($error instanceof mysqli_sql_exception ? 'Credit migration database failure; code='.$error->getCode() : $error->getMessage()).PHP_EOL);
    exit(1);
}
