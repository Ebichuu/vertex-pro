// Exceptions 页面的展示模型（UI-10）：UNKNOWN / 容量不可信等需人工处理的
// 异常与 §16.8 命令矩阵中的受控操作。本页绝无"一键再 Add"。

export interface ExceptionItem {
  id: string
  title: string
  downloader: string
  user_status: string
  user_status_label: string
}

// actionsForJob 按当前状态给出受控操作矩阵（§16.8）：
//   UNKNOWN: 全部四项；EXPIRED_UNKNOWN: 仅确认已存在与无副作用过期。
export function actionsForJob(status: string): ReadonlyArray<{ action: string; label: string; danger?: boolean }> {
  if (status === 'UNKNOWN') {
    return [
      { action: 'CONTINUE_RECONCILIATION', label: '继续对账' },
      { action: 'CONFIRM_EFFECT', label: '人工确认已存在' },
      { action: 'CONFIRM_NO_EFFECT_AND_RETRY', label: '确认无副作用并允许重试' },
      { action: 'EXPIRE_UNRESOLVED', label: '停止自动对账（进入需确认）', danger: true },
    ]
  }
  if (status === 'EXPIRED_UNKNOWN') {
    return [
      { action: 'CONFIRM_EFFECT', label: '人工确认已存在' },
      { action: 'CONFIRM_NO_EFFECT_AND_EXPIRE', label: '确认无副作用并结束', danger: true },
    ]
  }
  return []
}
