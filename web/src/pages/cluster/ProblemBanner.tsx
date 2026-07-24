import { CosInlineNotification } from '@cube-frontend/ui-library'
import { Problem } from '../../model/validate'

export const ProblemBanner = (props: { problems: Problem[] }) => {
  const issues = props.problems.filter((p) => p.level !== 'success')
  if (issues.length === 0) return null
  return (
    <div className="flex flex-col gap-y-2">
      {issues.map((p) => (
        <CosInlineNotification
          key={p.title}
          type={p.level === 'error' ? 'error' : 'warning'}
          title={p.title}
          isClosable={false}
        >
          {p.text}
        </CosInlineNotification>
      ))}
    </div>
  )
}
