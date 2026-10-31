import { useServices } from "../app/services.tsx";
import { useEffect } from "react";
import { Link, navigate } from "../app/router.ts";
import { PageHeader } from "./PageHeader.tsx";
import { moreItems } from "./navigation.tsx";
import styles from "./More.module.css";

export function MorePage() {
  const { live } = useServices();
  useEffect(() => {
    const media = window.matchMedia("(min-width: 761px)");
    const redirect = () => {
      if (media.matches) navigate("/", { replace: true });
    };
    redirect();
    media.addEventListener("change", redirect);
    return () => media.removeEventListener("change", redirect);
  }, []);
  return (
    <section className={styles.page}>
      <PageHeader title="更多" />
      <nav aria-label="更多导航" className={styles.list}>
        {moreItems.map(({ path, label, Icon }) => (
          <Link
            key={path}
            to={path}
            onClick={path === "/tasks" ? () => live.tasks.setProject("") : undefined}
          >
            <Icon />
            <span>{label}</span>
          </Link>
        ))}
      </nav>
    </section>
  );
}
