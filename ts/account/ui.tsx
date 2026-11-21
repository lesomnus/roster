import { useId, type ComponentProps, type ReactNode } from 'react'
import { Button, ButtonAnchor, Form, FormField, ThemeProvider, UIProvider, ValueMenu, useTheme, type ThemePreference } from '@lesomnus/nox'
import type { SignInControls } from '#lib/signin.js'

/** Nox supplies the field surface; native inputs still own values and validation. */
export function Field({ label, id, ...props }: ComponentProps<'input'> & { label: string }): ReactNode {
	const generated = useId()
	const at = id ?? generated
	return <FormField label={label} htmlFor={at}><input {...props} id={at} /></FormField>
}

export function ChoiceField({ label, id, ...props }: ComponentProps<'select'> & { label: string }): ReactNode {
	const generated = useId()
	const at = id ?? generated
	return <FormField label={label} htmlFor={at}><select {...props} id={at} /></FormField>
}

function SignInForm(props: ComponentProps<'form'>): ReactNode {
	return <Form columns={1} {...props} />
}

export const signInControls: SignInControls = { Button, Anchor: ButtonAnchor, Form: SignInForm, Field }

export function AccountUI({ children }: { children: ReactNode }): ReactNode {
	return <UIProvider><ThemeProvider defaultPreference="system">{children}</ThemeProvider></UIProvider>
}

export function ThemeChoice(): ReactNode {
	const { preference, setPreference } = useTheme()
	return <ValueMenu label="Appearance" value={preference} variant="compact" options={[
		{ value: 'system', label: 'System' },
		{ value: 'light', label: 'Light' },
		{ value: 'dark', label: 'Dark' },
	]} choose={(value) => setPreference(value as ThemePreference)} />
}
