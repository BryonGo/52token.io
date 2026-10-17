import type { QueryClient } from '@tanstack/react-query'

// Every group editor refreshes the same labels, effective multipliers and
// selectable-ID lists. Values stored on channels/tokens never become labels.
export function invalidateGroupQueries(client: QueryClient) {
  return Promise.all(
    [
      'managed-groups',
      'group-labels',
      'groups',
      'user-groups',
      'token-auto-groups',
      'pricing',
      'model-pricing-config',
      'model-pricing-preview',
    ].map((key) => client.invalidateQueries({ queryKey: [key] }))
  )
}
